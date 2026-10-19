// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title IIgneumFund
/// @notice The ecosystem fund's contract: a 4-of-7 multisig holding the fund's IGN under the published mandate
///         (docs/igneumhash/FUND-MANDATE-2026-10-11.md, section 5). Seven signers, four to spend, two to freeze,
///         five with a sixty-day notice to change a signer; a ninety-day spending ceiling of a quarter of the
///         balance at the window's start, lifted only by a notice executed sixty days earlier; an on-chain recusal;
///         a company-connected flag on each signer with at most three flagged and no spend carried by a flagged
///         majority. Every act is an event; policy refusals are events too, not reverts, so the accounts page is a
///         read of the log. No owner, no proxy, no pause beyond the freeze, no fee, no upgrade path; the contract
///         never holds a bond, never votes, never sends to itself.
interface IIgneumFund {
    enum Kind {
        Spend, // payee, amount, scopeHash, expiry; 4 of 7; the ceiling, the freeze and the majority check apply
        Notice, // amount, scopeHash; 4 of 7; sixty days after execution it lets one Spend above the ceiling
        Freeze, // 2 of 7; thirty days from execution
        Lift, // 4 of 7, fresh (made after the freeze); ends the freeze
        SetFlag, // signer, flag; 4 of 7; at most three flagged after it
        Replace, // oldSigner, newSigner, newFlag; the change quorum, then sixty days; composition checked at execution
        Migrate, // successor; the change quorum, then sixty days; the whole balance to a successor contract under a new mandate
        Add // newSigner, newFlag; the change quorum of the current size, then sixty days; the set grows by one (GOVERNANCE 8)
    }

    struct Proposal {
        Kind kind;
        address proposer;
        address payee; // Spend: the payee; SetFlag and Replace: the signer concerned; Migrate: the successor; Add: the new signer
        address newSigner; // Replace only
        uint256 amount; // Spend and Notice
        bytes32 scopeHash; // Spend and Notice: the hash of the published scope and milestone
        uint64 expiry; // 0 for none; a proposal past it cannot execute
        uint64 createdAt;
        uint64 readyAt; // Replace, Migrate and Add: set when the change quorum lands, sixty days later
        uint64 noticeId; // Spend: the Notice it relies on (0 for none)
        bool flag; // SetFlag: the flag to set; Replace: the new signer's flag
        bool companyPayee; // Spend: the payee is the company or company-connected (set at proposal or marked by any signer; never cleared)
        bool closed; // executed or refused
        uint8 approvals; // live count of approving signers (recused ones are subtracted at execution)
    }

    event Proposed(uint64 indexed id, Kind kind, address indexed proposer, address payee, uint256 amount, bytes32 scopeHash, uint64 expiry);
    event Approved(uint64 indexed id, address indexed signer, uint8 approvals);
    event ApprovalRevoked(uint64 indexed id, address indexed signer, uint8 approvals);
    event Recused(uint64 indexed id, address indexed signer);
    event Executed(uint64 indexed id, Kind kind, address payee, uint256 amount);
    event Refused(uint64 indexed id, Kind kind, string reason);
    event CompanyPayeeMarked(uint64 indexed id, address indexed by);
    event CompanyPayeeApprovalsExcluded(uint64 indexed id, address[] excluded, uint256 counted, uint256 quorum);
    event ReplaceReady(uint64 indexed id, uint64 readyAt);
    event Migrated(uint64 indexed id, address indexed successor, uint256 amount);
    event Frozen(uint64 indexed id, uint64 until);
    event Lifted(uint64 indexed id);
    event FlagSet(address indexed signer, bool companyConnected);
    event SignerReplaced(address indexed oldSigner, address indexed newSigner, bool companyConnected);
    event WindowOpened(uint64 indexed index, uint256 balanceAtStart, uint256 ceiling);
    event Received(address indexed from, uint256 amount);

    error BadSigner(address signer);
    error TooManyCompanyConnected(uint256 count);
    error OnlySigner(address caller);
    error NoSuchProposal(uint64 id);
    error ProposalClosed(uint64 id);
    error PastExpiry(uint64 id, uint64 expiry);
    error BelowQuorum(uint64 id, uint256 approvals, uint256 quorum);
    error NotReady(uint64 id, uint64 readyAt);
    error BadInput(string what);

    /// @notice The set's size and the quorums at that size (GOVERNANCE 8's table; they step with the size as the
    ///         set grows by Add). The largest size the table knows is seven.
    function SIGNERS() external view returns (uint256);
    function SPEND_QUORUM() external view returns (uint256);
    function FREEZE_QUORUM() external view returns (uint256);
    function CHANGE_QUORUM() external view returns (uint256);
    /// @notice The migrate quorum: every signer below five (3 of 3, 4 of 4), the change quorum from five.
    function MIGRATE_QUORUM() external view returns (uint256);
    /// @notice The replace quorum: every signer below five (3 of 3, 4 of 4), the change quorum from five.
    function REPLACE_QUORUM() external view returns (uint256);
    function MAX_SIGNERS() external pure returns (uint256);
    function quorumsAt(uint256 size) external pure returns (uint256 spend, uint256 freeze, uint256 change, uint256 maxCompanyConnected);
    function proposeAdd(address newSigner, bool newFlag) external returns (uint64 id);
    event AddReady(uint64 indexed id, uint64 readyAt);
    event SignerAdded(address indexed newSigner, bool companyConnected, uint256 size);
    function WINDOW() external pure returns (uint64);
    function NOTICE_DELAY() external pure returns (uint64);
    function FREEZE_DURATION() external pure returns (uint64);
    function MAX_COMPANY_CONNECTED() external view returns (uint256);

    function signers() external view returns (address[] memory);
    function isSigner(address who) external view returns (bool);
    function companyConnected(address signer) external view returns (bool);
    function proposals(uint64 id) external view returns (Proposal memory);
    function approved(uint64 id, address signer) external view returns (bool);
    function recused(uint64 id, address signer) external view returns (bool);
    function frozenUntil() external view returns (uint64);
    function frozenAt() external view returns (uint64);
    function noticeExecutedAt(uint64 id) external view returns (uint64);
    function noticeSpent(uint64 id) external view returns (bool);
    function windowIndex() external view returns (uint64);
    function windowBalanceAtStart() external view returns (uint256);
    function windowSpent() external view returns (uint256);
    function deployedAt() external view returns (uint64);
    function proposalCount() external view returns (uint64);

    function proposeSpend(address payee, uint256 amount, bytes32 scopeHash, uint64 expiry, uint64 noticeId, bool companyPayee) external returns (uint64 id);
    function markCompanyPayee(uint64 id) external;
    function proposeNotice(uint256 amount, bytes32 scopeHash, uint64 expiry) external returns (uint64 id);
    function proposeFreeze() external returns (uint64 id);
    function proposeLift() external returns (uint64 id);
    function proposeSetFlag(address signer, bool flag) external returns (uint64 id);
    function proposeReplace(address oldSigner, address newSigner, bool newFlag) external returns (uint64 id);
    function proposeMigrate(address successor) external returns (uint64 id);
    function approve(uint64 id) external;
    function revokeApproval(uint64 id) external;
    function recuse(uint64 id) external;
    function execute(uint64 id) external;
    function openWindow() external;
}
