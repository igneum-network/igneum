// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";

import {IIgneumFund} from "../interfaces/IIgneumFund.sol";

/// @title IgneumFund
/// @notice The ecosystem fund under its mandate (docs/igneumhash/FUND-MANDATE-2026-10-11.md, section 5). The chain
///         credits the fund's address in Finalize; this contract is that address and holds IGN and nothing else.
/// @dev The forms, each against the mandate's item:
///      - Spend (4 of 7): payee, amount, scope hash, expiry. Refused by event, not revert, when the fund is frozen,
///        when the approving signers (less any recused) are a majority company-connected, or when the ninety-day
///        window's spending would pass a quarter of the balance at the window's start without a Notice executed
///        sixty days earlier (the Notice names the amount it covers; a Spend cites it; one Spend per Notice).
///      - Freeze (2 of 7): thirty days from execution; a Lift (4 of 7, proposed after the freeze) ends it early.
///      - Recusal: a signer's recusal on a proposal removes that signer's approval from the quorum and from the
///        company-majority check; it is an event.
///      - SetFlag (4 of 7): a signer's company-connected flag; at most three flagged after it.
///      - Replace (every key below five, the change quorum from five, then sixty days): one signer out, one in, the new flag given; the composition rule
///        (at most three flagged) checked at execution. The set stays seven.
///      - The window: ninety days from the contract's deployment, consecutive; the balance at the window's start is
///        taken at the window's first act (a Spend or `openWindow`, which anyone may call at the boundary), and the
///        window's ceiling is a quarter of it. Credits arriving after the snapshot do not raise the ceiling.
///      - Every proposal, approval, revocation, recusal, execution, refusal, freeze, lift, flag and signer change
///        is an event with its reason. No owner, no proxy, no pause beyond the freeze, no fee, no upgrade path; no
///        bond, no vote, no send to itself, no spend beyond the balance.
contract IgneumFund is IIgneumFund, ReentrancyGuard {
    /// @dev The set's size and the quorums at that size (GOVERNANCE 8's table, the project's order of 12 October 2026:
    ///      the fund opens on the founder's three and grows to the mandate's seven by Add). They are state, read
    ///      again from the table whenever the set's size changes; the table is the contract's own and fixed.
    uint256 public constant override MAX_SIGNERS = 7;
    uint256 public override SIGNERS;
    uint256 public override SPEND_QUORUM;
    uint256 public override FREEZE_QUORUM;
    uint256 public override CHANGE_QUORUM;
    /// @dev The migrate quorum (the security review's reading of the sized form, 12 October 2026): a Migrate moves the WHOLE
    ///      balance outside the ninety-day ceiling, so below five signers it needs every key (3 of 3, 4 of 4); from
    ///      five it is the table's change quorum. Replace and Add keep the change quorum at every size.
    uint256 public override MIGRATE_QUORUM;
    /// @dev The replace quorum (the project's ruling of 12 October 2026, both halves of the security review's reading): a Replace
    ///      below five also needs every key, else a pair at three replaces the honest third and then migrates with
    ///      its own; from five it is the table's change quorum. Add stays at the change quorum (it only enlarges
    ///      the set toward five).
    uint256 public override REPLACE_QUORUM;
    uint256 public override MAX_COMPANY_CONNECTED;
    uint64 public constant override WINDOW = 90 days;
    uint64 public constant override NOTICE_DELAY = 60 days;
    uint64 public constant override FREEZE_DURATION = 30 days;

    address[] private _signers;
    mapping(address => bool) public override isSigner;
    mapping(address => bool) public override companyConnected;

    uint64 public override proposalCount;
    mapping(uint64 => Proposal) private _proposals;
    mapping(uint64 => mapping(address => bool)) public override approved;
    mapping(uint64 => mapping(address => bool)) public override recused;

    uint64 public override frozenUntil;
    uint64 public override frozenAt;
    mapping(uint64 => uint64) public override noticeExecutedAt;
    mapping(uint64 => bool) public override noticeSpent;

    uint64 public immutable override deployedAt;
    uint64 public override windowIndex;
    uint256 public override windowBalanceAtStart;
    uint256 public override windowSpent;
    bool private _windowOpen;

    modifier onlySigner() {
        if (!isSigner[msg.sender]) revert OnlySigner(msg.sender);
        _;
    }

    /// @param signers_ the signers, three to seven, distinct, none zero
    /// @param companyConnected_ each signer's flag; at most the table's bound true at that size
    constructor(address[] memory signers_, bool[] memory companyConnected_) {
        uint256 n = signers_.length;
        if (n < 3 || n > MAX_SIGNERS || companyConnected_.length != n) revert BadInput("three to seven signers and as many flags");
        _setSize(n);
        uint256 flagged = 0;
        for (uint256 i = 0; i < n; i++) {
            address s = signers_[i];
            // a bad signer list is a construction error; reverting in the loop is the point
            // forge-lint: disable-next-line(require-revert-in-loop)
            if (s == address(0) || isSigner[s]) revert BadSigner(s);
            isSigner[s] = true;
            _signers.push(s);
            if (companyConnected_[i]) {
                companyConnected[s] = true;
                flagged++;
            }
            emit FlagSet(s, companyConnected_[i]);
        }
        if (flagged > MAX_COMPANY_CONNECTED) revert TooManyCompanyConnected(flagged);
        // forge-lint: disable-next-line(unsafe-typecast)
        deployedAt = uint64(block.timestamp);
    }

    /// @inheritdoc IIgneumFund
    /// @dev GOVERNANCE 8's table: at every size the mandate's three bounds hold (no company majority, the ceiling,
    ///      the sixty-day notice). spend, freeze, change (add, replace, migrate), the company-connected bound.
    function quorumsAt(uint256 size) public pure override returns (uint256 spend, uint256 freeze, uint256 change, uint256 maxCompanyConnected) {
        if (size == 3) return (2, 1, 2, 1);
        if (size == 4) return (3, 1, 3, 1);
        if (size == 5) return (3, 2, 4, 2);
        if (size == 6) return (4, 2, 5, 2);
        if (size == 7) return (4, 2, 5, 3);
        revert BadInput("the size");
    }

    function _setSize(uint256 size) private {
        (uint256 spend, uint256 freeze, uint256 change, uint256 maxCc) = quorumsAt(size);
        SIGNERS = size;
        SPEND_QUORUM = spend;
        FREEZE_QUORUM = freeze;
        CHANGE_QUORUM = change;
        MIGRATE_QUORUM = size < 5 ? size : change;
        REPLACE_QUORUM = size < 5 ? size : change;
        MAX_COMPANY_CONNECTED = maxCc;
    }

    /// @notice The chain credits the fund in Finalize without a transaction; a transfer by anyone is kept too.
    receive() external payable {
        emit Received(msg.sender, msg.value);
    }

    // ---------------------------------------------------------------- proposals

    /// @inheritdoc IIgneumFund
    function proposeSpend(address payee, uint256 amount, bytes32 scopeHash, uint64 expiry, uint64 noticeId, bool companyPayee)
        external
        override
        onlySigner
        returns (uint64 id)
    {
        if (payee == address(0) || payee == address(this)) revert BadInput("the payee");
        if (amount == 0) revert BadInput("the amount");
        if (scopeHash == bytes32(0)) revert BadInput("the scope");
        if (noticeId != 0 && noticeExecutedAt[noticeId] == 0) revert BadInput("the notice");
        id = _open(Kind.Spend, payee, amount, scopeHash, expiry);
        _proposals[id].noticeId = noticeId;
        if (companyPayee) {
            _proposals[id].companyPayee = true;
            emit CompanyPayeeMarked(id, msg.sender);
        }
    }

    /// @inheritdoc IIgneumFund
    /// @dev Any signer may mark a live Spend's payee as the company's (the mandate: Labs cannot approve its own
    ///      contract; fund-ops' finding of 12 October 2026); the mark never clears, so a proposer who left it off
    ///      is corrected by one signer before execution.
    function markCompanyPayee(uint64 id) external override onlySigner {
        Proposal storage p = _live(id);
        if (p.kind != Kind.Spend) revert BadInput("not a spend");
        if (p.companyPayee) return;
        p.companyPayee = true;
        emit CompanyPayeeMarked(id, msg.sender);
    }

    /// @inheritdoc IIgneumFund
    function proposeNotice(uint256 amount, bytes32 scopeHash, uint64 expiry) external override onlySigner returns (uint64 id) {
        if (amount == 0) revert BadInput("the amount");
        if (scopeHash == bytes32(0)) revert BadInput("the scope");
        id = _open(Kind.Notice, address(0), amount, scopeHash, expiry);
    }

    /// @inheritdoc IIgneumFund
    function proposeFreeze() external override onlySigner returns (uint64 id) {
        id = _open(Kind.Freeze, address(0), 0, bytes32(0), 0);
    }

    /// @inheritdoc IIgneumFund
    function proposeLift() external override onlySigner returns (uint64 id) {
        if (block.timestamp >= frozenUntil) revert BadInput("not frozen");
        id = _open(Kind.Lift, address(0), 0, bytes32(0), 0);
    }

    /// @inheritdoc IIgneumFund
    function proposeSetFlag(address signer, bool flag) external override onlySigner returns (uint64 id) {
        if (!isSigner[signer]) revert BadSigner(signer);
        id = _open(Kind.SetFlag, signer, 0, bytes32(0), 0);
        _proposals[id].flag = flag;
    }

    /// @inheritdoc IIgneumFund
    function proposeReplace(address oldSigner, address newSigner, bool newFlag) external override onlySigner returns (uint64 id) {
        if (!isSigner[oldSigner]) revert BadSigner(oldSigner);
        if (newSigner == address(0) || isSigner[newSigner] || newSigner == address(this)) revert BadSigner(newSigner);
        id = _open(Kind.Replace, oldSigner, 0, bytes32(0), 0);
        _proposals[id].newSigner = newSigner;
        _proposals[id].flag = newFlag;
    }

    /// @inheritdoc IIgneumFund
    /// @dev The one act that empties the fund: a new mandate's successor contract takes the whole balance. Five of
    ///      seven, then sixty days from the fifth approval (the signer-change class, economics's read of the
    ///      mandate); the freeze and the company-majority check apply at execution; the successor must be a
    ///      contract, never this one and never a signer's account.
    function proposeMigrate(address successor) external override onlySigner returns (uint64 id) {
        if (successor == address(0) || successor == address(this) || successor.code.length == 0 || isSigner[successor]) {
            revert BadInput("the successor");
        }
        id = _open(Kind.Migrate, successor, 0, bytes32(0), 0);
    }

    /// @inheritdoc IIgneumFund
    /// @dev A seat added at the current change quorum, executed sixty days on (GOVERNANCE 8 rule 1): the new
    ///      signer named against the composition of the next size; the set never passes seven.
    function proposeAdd(address newSigner, bool newFlag) external override onlySigner returns (uint64 id) {
        if (newSigner == address(0) || isSigner[newSigner]) revert BadSigner(newSigner);
        if (_signers.length >= MAX_SIGNERS) revert BadInput("the set is seven");
        id = _open(Kind.Add, newSigner, 0, bytes32(0), 0);
        _proposals[id].flag = newFlag;
    }

    function _open(Kind kind, address payee, uint256 amount, bytes32 scopeHash, uint64 expiry) private returns (uint64 id) {
        if (expiry != 0 && expiry <= block.timestamp) revert BadInput("the expiry");
        id = ++proposalCount;
        Proposal storage p = _proposals[id];
        p.kind = kind;
        p.proposer = msg.sender;
        p.payee = payee;
        p.amount = amount;
        p.scopeHash = scopeHash;
        p.expiry = expiry;
        // forge-lint: disable-next-line(unsafe-typecast)
        p.createdAt = uint64(block.timestamp);
        emit Proposed(id, kind, msg.sender, payee, amount, scopeHash, expiry);
    }

    // ---------------------------------------------------------------- approvals and recusal

    /// @inheritdoc IIgneumFund
    function approve(uint64 id) external override onlySigner {
        Proposal storage p = _live(id);
        if (recused[id][msg.sender]) revert BadInput("recused");
        if (approved[id][msg.sender]) return;
        approved[id][msg.sender] = true;
        p.approvals++;
        emit Approved(id, msg.sender, p.approvals);
        if ((p.kind == Kind.Replace || p.kind == Kind.Migrate || p.kind == Kind.Add) && p.readyAt == 0 && p.approvals >= _quorum(p.kind)) {
            // forge-lint: disable-next-line(unsafe-typecast)
            p.readyAt = uint64(block.timestamp) + NOTICE_DELAY;
            emit ReplaceReady(id, p.readyAt);
        }
    }

    /// @inheritdoc IIgneumFund
    function revokeApproval(uint64 id) external override onlySigner {
        Proposal storage p = _live(id);
        if (!approved[id][msg.sender]) return;
        // the event for this change is emitted below in the same call
        // forge-lint: disable-next-line(missing-events-access-control)
        approved[id][msg.sender] = false;
        p.approvals--;
        emit ApprovalRevoked(id, msg.sender, p.approvals);
    }

    /// @inheritdoc IIgneumFund
    /// @dev A recusal stands: it cannot be undone on this proposal, and it removes any approval already given.
    function recuse(uint64 id) external override onlySigner {
        Proposal storage p = _live(id);
        if (recused[id][msg.sender]) return;
        recused[id][msg.sender] = true;
        if (approved[id][msg.sender]) {
            // the event for this change is emitted below in the same call
            // forge-lint: disable-next-line(missing-events-access-control)
            approved[id][msg.sender] = false;
            p.approvals--;
        }
        emit Recused(id, msg.sender);
    }

    // ---------------------------------------------------------------- execution

    /// @inheritdoc IIgneumFund
    /// @dev Preconditions (a closed or expired proposal, a quorum not reached, a replace before its day) revert;
    ///      the mandate's refusals (frozen, the ceiling, the company majority, the composition) close the proposal
    ///      with a Refused event so the log carries the reason.
    function execute(uint64 id) external override nonReentrant onlySigner {
        Proposal storage p = _live(id);
        (uint256 count, uint256 flagged) = _countApprovals(id);
        uint256 quorum = _quorum(p.kind);
        if (count < quorum) revert BelowQuorum(id, count, quorum);
        if (p.kind == Kind.Spend) {
            _executeSpend(id, p, count, flagged);
        } else if (p.kind == Kind.Notice) {
            if (block.timestamp < frozenUntil) {
                _refuse(id, p, "frozen");
                return;
            }
            p.closed = true;
            // forge-lint: disable-next-line(unsafe-typecast)
            noticeExecutedAt[id] = uint64(block.timestamp);
            emit Executed(id, Kind.Notice, address(0), p.amount);
        } else if (p.kind == Kind.Freeze) {
            p.closed = true;
            // forge-lint: disable-next-line(unsafe-typecast)
            frozenAt = uint64(block.timestamp);
            frozenUntil = frozenAt + FREEZE_DURATION;
            emit Frozen(id, frozenUntil);
            emit Executed(id, Kind.Freeze, address(0), 0);
        } else if (p.kind == Kind.Lift) {
            if (p.createdAt < frozenAt) {
                _refuse(id, p, "a lift made before the freeze is not fresh");
                return;
            }
            p.closed = true;
            frozenUntil = 0;
            emit Lifted(id);
            emit Executed(id, Kind.Lift, address(0), 0);
        } else if (p.kind == Kind.SetFlag) {
            if (!isSigner[p.payee]) {
                _refuse(id, p, "no longer a signer");
                return;
            }
            if (p.flag && !companyConnected[p.payee] && _flaggedCount() + 1 > MAX_COMPANY_CONNECTED) {
                _refuse(id, p, "more company-connected signers than the size allows");
                return;
            }
            p.closed = true;
            companyConnected[p.payee] = p.flag;
            emit FlagSet(p.payee, p.flag);
            emit Executed(id, Kind.SetFlag, p.payee, 0);
        } else if (p.kind == Kind.Replace) {
            _executeReplace(id, p);
        } else if (p.kind == Kind.Add) {
            _executeAdd(id, p);
        } else {
            _executeMigrate(id, p, count, flagged);
        }
    }

    /// @dev The seat added sixty days after the change quorum landed: the composition of the NEXT size is the
    ///      check (the table's company-connected bound at size plus one), the quorums step to the new size.
    function _executeAdd(uint64 id, Proposal storage p) private {
        if (p.readyAt == 0 || block.timestamp < p.readyAt) revert NotReady(id, p.readyAt);
        address newSigner = p.payee;
        if (isSigner[newSigner]) {
            _refuse(id, p, "already a signer");
            return;
        }
        if (_signers.length >= MAX_SIGNERS) {
            _refuse(id, p, "the set is seven");
            return;
        }
        (,,, uint256 maxCcNext) = quorumsAt(_signers.length + 1);
        if (p.flag && _flaggedCount() + 1 > maxCcNext) {
            _refuse(id, p, "more company-connected signers than the next size allows");
            return;
        }
        p.closed = true;
        isSigner[newSigner] = true;
        _signers.push(newSigner);
        companyConnected[newSigner] = p.flag;
        _setSize(_signers.length);
        emit FlagSet(newSigner, p.flag);
        emit SignerAdded(newSigner, p.flag, _signers.length);
        emit Executed(id, Kind.Add, newSigner, 0);
    }

    function _executeMigrate(uint64 id, Proposal storage p, uint256 count, uint256 flagged) private {
        if (p.readyAt == 0 || block.timestamp < p.readyAt) revert NotReady(id, p.readyAt);
        if (block.timestamp < frozenUntil) {
            _refuse(id, p, "frozen");
            return;
        }
        if (flagged * 2 > count) {
            _refuse(id, p, "the approving signers are a majority company-connected");
            return;
        }
        if (p.payee.code.length == 0) {
            _refuse(id, p, "the successor is not a contract");
            return;
        }
        p.closed = true;
        uint256 amount = address(this).balance;
        emit Migrated(id, p.payee, amount);
        emit Executed(id, Kind.Migrate, p.payee, amount);
        (bool ok,) = p.payee.call{value: amount}("");
        if (!ok) revert BadInput("the successor refused the balance");
    }

    function _executeSpend(uint64 id, Proposal storage p, uint256 count, uint256 flagged) private {
        if (block.timestamp < frozenUntil) {
            _refuse(id, p, "frozen");
            return;
        }
        if (p.companyPayee) {
            // a payment to the company: no company-connected approval counts, the quorum is taken over the
            // unflagged signers alone, and the exclusions are on the log (the mandate's "Labs cannot approve its
            // own contract", enforced by the contract, not an operator's process)
            address[] memory excluded = new address[](flagged);
            uint256 k = 0;
            for (uint256 i = 0; i < _signers.length; i++) {
                address sgn = _signers[i];
                if (approved[id][sgn] && !recused[id][sgn] && companyConnected[sgn]) excluded[k++] = sgn;
            }
            uint256 counted = count - flagged;
            emit CompanyPayeeApprovalsExcluded(id, excluded, counted, SPEND_QUORUM);
            if (counted < SPEND_QUORUM) {
                _refuse(id, p, "a payment to the company needs the spend quorum of approvals from signers not connected to it");
                return;
            }
        } else if (flagged * 2 > count) {
            _refuse(id, p, "the approving signers are a majority company-connected");
            return;
        }
        if (p.amount > address(this).balance) {
            _refuse(id, p, "beyond the balance");
            return;
        }
        _settleWindow();
        uint256 ceiling = windowBalanceAtStart / 4;
        uint256 covered = 0;
        if (p.noticeId != 0) {
            uint64 at = noticeExecutedAt[p.noticeId];
            if (noticeSpent[p.noticeId]) {
                _refuse(id, p, "the notice is spent");
                return;
            }
            if (block.timestamp < at + NOTICE_DELAY) {
                _refuse(id, p, "the notice is younger than sixty days");
                return;
            }
            covered = _proposals[p.noticeId].amount;
            if (covered > p.amount) covered = p.amount;
        }
        if (windowSpent + (p.amount - covered) > ceiling) {
            _refuse(id, p, "above the window's ceiling without a notice");
            return;
        }
        p.closed = true;
        windowSpent += p.amount - covered;
        if (p.noticeId != 0) noticeSpent[p.noticeId] = true;
        emit Executed(id, Kind.Spend, p.payee, p.amount);
        (bool ok,) = p.payee.call{value: p.amount}("");
        if (!ok) revert BadInput("the payee refused the payment");
    }

    function _executeReplace(uint64 id, Proposal storage p) private {
        if (p.readyAt == 0 || block.timestamp < p.readyAt) revert NotReady(id, p.readyAt);
        address oldSigner = p.payee;
        address newSigner = p.newSigner;
        if (!isSigner[oldSigner] || isSigner[newSigner]) {
            _refuse(id, p, "the set changed under the proposal");
            return;
        }
        uint256 flaggedAfter = _flaggedCount() - (companyConnected[oldSigner] ? 1 : 0) + (p.flag ? 1 : 0);
        if (flaggedAfter > MAX_COMPANY_CONNECTED) {
            _refuse(id, p, "more company-connected signers than the size allows");
            return;
        }
        p.closed = true;
        // the event for this change is emitted below in the same call
        // forge-lint: disable-next-line(missing-events-access-control)
        isSigner[oldSigner] = false;
        companyConnected[oldSigner] = false;
        isSigner[newSigner] = true;
        companyConnected[newSigner] = p.flag;
        for (uint256 i = 0; i < _signers.length; i++) {
            if (_signers[i] == oldSigner) {
                _signers[i] = newSigner;
                break;
            }
        }
        emit SignerReplaced(oldSigner, newSigner, p.flag);
        emit FlagSet(newSigner, p.flag);
        emit Executed(id, Kind.Replace, newSigner, 0);
    }

    function _refuse(uint64 id, Proposal storage p, string memory reason) private {
        p.closed = true;
        emit Refused(id, p.kind, reason);
    }

    // ---------------------------------------------------------------- the window

    /// @inheritdoc IIgneumFund
    /// @notice Anyone may open the current window at its boundary, so the balance at its start is taken then
    ///         rather than at the first spend.
    function openWindow() external override {
        _settleWindow();
    }

    function _settleWindow() private {
        // forge-lint: disable-next-line(unsafe-typecast)
        uint64 index = (uint64(block.timestamp) - deployedAt) / WINDOW;
        if (_windowOpen && index == windowIndex) return;
        windowIndex = index;
        windowBalanceAtStart = address(this).balance;
        windowSpent = 0;
        _windowOpen = true;
        emit WindowOpened(index, windowBalanceAtStart, windowBalanceAtStart / 4);
    }

    // ---------------------------------------------------------------- views

    /// @inheritdoc IIgneumFund
    function signers() external view override returns (address[] memory) {
        return _signers;
    }

    /// @inheritdoc IIgneumFund
    function proposals(uint64 id) external view override returns (Proposal memory) {
        return _proposals[id];
    }

    // ---------------------------------------------------------------- internal

    function _live(uint64 id) private view returns (Proposal storage p) {
        if (id == 0 || id > proposalCount) revert NoSuchProposal(id);
        p = _proposals[id];
        if (p.closed) revert ProposalClosed(id);
        if (p.expiry != 0 && block.timestamp > p.expiry) revert PastExpiry(id, p.expiry);
    }

    function _quorum(Kind kind) private view returns (uint256) {
        if (kind == Kind.Freeze) return FREEZE_QUORUM;
        if (kind == Kind.Migrate) return MIGRATE_QUORUM;
        if (kind == Kind.Replace) return REPLACE_QUORUM;
        if (kind == Kind.Add) return CHANGE_QUORUM;
        return SPEND_QUORUM;
    }

    /// @dev Approvals that count: by a current signer, not recused. Recusal already removed the approval, but a
    ///      signer replaced since approving is dropped here.
    function _countApprovals(uint64 id) private view returns (uint256 count, uint256 flagged) {
        for (uint256 i = 0; i < _signers.length; i++) {
            address s = _signers[i];
            if (!approved[id][s] || recused[id][s]) continue;
            count++;
            if (companyConnected[s]) flagged++;
        }
    }

    function _flaggedCount() private view returns (uint256 n) {
        for (uint256 i = 0; i < _signers.length; i++) {
            if (companyConnected[_signers[i]]) n++;
        }
    }
}
