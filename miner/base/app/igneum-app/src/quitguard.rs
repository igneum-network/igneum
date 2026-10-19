//! A quit that quits (the second Windows machine, 8 October 2026, 21:24 UK: api/quit on an earlier release left all four processes up 90 s later while the
//! node sat in initial sync; the founder's plug-tune-play rule says no fault a user fixes by hand). The engine's own
//! quit path stops the miners, then the node, by their process handles with a short grace and a kill; this module is
//! the bound over the whole of it: a guard armed the moment a quit is asked, which, when the process is still here
//! after QUIT_BOUND_S whatever the engine is doing (a tick blocked on a syncing node's RPC, a stop that never
//! answers), ends the node and the workers by the pids the state records, never by name, prints the exit line and
//! leaves. The window reads the stage words from `quit_stage` meanwhile.

use std::sync::Arc;

/// The whole quit, whatever the node's state: miners, node, the last upload. Past this the guard ends what is left.
pub const QUIT_BOUND_S: u64 = 45;
/// The node's grace after its stop signal before the kill (30 before an earlier release; a syncing node ignored it for longer).
pub const NODE_GRACE_S: u64 = 10;
/// How long the quit waits for the last log upload.
pub const UPLOAD_WAIT_S: u64 = 10;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Stage {
    Miners,
    Node,
    Logs,
    Forced,
}

impl Stage {
    pub fn words(self) -> &'static str {
        match self {
            Stage::Miners => "quitting: the miners are being stopped",
            Stage::Node => "quitting: the chain connection is being closed",
            Stage::Logs => "quitting: the last log upload",
            Stage::Forced => "quitting: the chain connection did not close in time; ending it by pid",
        }
    }
}

/// The clock over a quit (seconds since the engine started, the engine's own `secs`).
pub struct Guard {
    armed_at: f64,
}

impl Guard {
    pub fn new(now: f64) -> Guard {
        Guard { armed_at: now }
    }
    pub fn overdue(&self, now: f64) -> bool {
        now - self.armed_at >= QUIT_BOUND_S as f64
    }
}

/// The pids the state records: the node's and every card's worker (0 = none).
pub fn recorded_pids(st: &crate::state::State) -> Vec<(&'static str, u32)> {
    let mut v = Vec::new();
    if st.node.pid > 0 {
        v.push(("node", st.node.pid));
    }
    for c in &st.mining.cards {
        if c.pid > 0 {
            v.push(("worker", c.pid));
        }
    }
    v
}

/// Ends each pid (platform::kill_pid: taskkill /PID /T /F on Windows, SIGKILL elsewhere); the record lines.
pub fn enforce_pids(pids: &[(&str, u32)]) -> Vec<String> {
    pids.iter()
        .map(|(what, pid)| {
            crate::platform::kill_pid(*pid);
            format!("QUIT event=forced what={what} pid={pid}")
        })
        .collect()
}

/// Whether a pid is still a live process (unix: kill -0; Windows: tasklist by pid).
pub fn pid_alive(pid: u32) -> bool {
    #[cfg(unix)]
    {
        unsafe { libc::kill(pid as i32, 0) == 0 }
    }
    #[cfg(not(unix))]
    {
        let out = crate::platform::quiet(&mut std::process::Command::new(crate::platform::tool("tasklist"))).args(["/FI", &format!("PID eq {pid}"), "/NH"]).output();
        out.map(|o| String::from_utf8_lossy(&o.stdout).contains(&pid.to_string())).unwrap_or(false)
    }
}

/// Sets the stage words the window shows.
pub fn set_stage(shared: &crate::engine::Shared, stage: Stage) {
    let mut st = shared.state.lock().unwrap();
    st.quitting = true;
    st.quit_stage = stage.words().into();
}

/// Arms the guard once per process (the server's /api/quit and the engine's Cmd::Quit both call it): a thread that,
/// QUIT_BOUND_S after the ask, ends the recorded pids and leaves with the exit line, when the engine has not left
/// by itself. `exit_line` is what the window host reads ("EXIT" or "EXIT update").
pub fn arm(shared: Arc<crate::engine::Shared>, exit_line: &'static str) {
    if shared.quit_armed.swap(true, std::sync::atomic::Ordering::SeqCst) {
        return;
    }
    std::thread::Builder::new()
        .name("quit-guard".into())
        .spawn(move || {
            std::thread::sleep(std::time::Duration::from_secs(QUIT_BOUND_S));
            let pids = recorded_pids(&shared.state.lock().unwrap());
            shared.log(&format!("quit guard: still here {QUIT_BOUND_S} s after the ask; ending {} recorded process(es) by pid and leaving", pids.len()));
            set_stage(&shared, Stage::Forced);
            for l in enforce_pids(&pids) {
                shared.log(&l);
            }
            shared.save_settings();
            shared.log("stopped (by the quit guard)");
            println!("{exit_line}");
            use std::io::Write;
            let _ = std::io::stdout().flush();
            std::process::exit(0);
        })
        .expect("quit guard thread");
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Known failed first: a node that never answers its stop (here a child that ignores SIGTERM, as a node stuck in
    /// initial sync did on the second Windows machine) is ended by its pid within the grace plus a second, never left running.
    #[test]
    fn a_node_that_never_answers_its_stop_is_ended_by_pid_within_the_bound() {
        if cfg!(windows) {
            return;
        }
        let (tx, _rx) = std::sync::mpsc::channel();
        let log = std::env::temp_dir().join(format!("igneum-quit-node-{}.log", std::process::id()));
        let mut p = crate::procs::spawn(crate::procs::Source::Node, std::path::Path::new("sh"), &["-c".into(), "trap '' TERM; while :; do sleep 1; done".into()], None, &log, &tx, &[]).unwrap();
        std::thread::sleep(std::time::Duration::from_millis(300));
        let pid = p.pid();
        let started = std::time::Instant::now();
        let code = p.stop(NODE_GRACE_S.min(2));
        assert!(started.elapsed() < std::time::Duration::from_secs(4), "the stop took {:?}", started.elapsed());
        assert_eq!(code, Some(-9), "the grace ran out and the kill ended it");
        assert!(!pid_alive(pid), "pid {pid} is still here");
        let _ = std::fs::remove_file(log);
    }

    /// Known failed first (the second Windows machine record #1789, 8 October 2026, 21:24 UK, read at 22:0x): the old engine reached its SUMMARY
    /// line one second after api/quit and then ended inside the last log upload with no "stopped" line and no EXIT line
    /// on its stdout, so the host read a plain death and started a new engine 35 s later. The EXIT line is the host's
    /// one word that a quit is meant to hold; it goes out BEFORE the last upload (the host only closes its handles on
    /// it, the engine finishes the upload on its own), so nothing that happens in the upload can turn a quit into a
    /// restart; and the print cannot panic on a pipe the host has already closed.
    #[test]
    fn the_exit_line_reaches_the_host_before_the_last_upload_and_never_panics_on_a_closed_pipe() {
        let s = include_str!("engine.rs");
        let shutdown = s.find("fn shutdown(&mut self)").expect("shutdown");
        let body = &s[shutdown..];
        let body = &body[..body.find("\n    }\n").expect("the end of shutdown")];
        let exit = body.find("print_exit_lines(").expect("the exit lines are printed through print_exit_lines");
        let upload = body.find("self.upload_logs(true)").expect("the last upload");
        assert!(exit < upload, "the EXIT line must go to the host before the last upload");
        assert!(!body[exit..].contains("println!"), "nothing is printed with println! after the host may have gone");
        let f = s.find("fn print_exit_lines(").expect("print_exit_lines");
        let fb = &s[f..f + 900];
        assert!(fb.contains("let _ = writeln!(") && !fb.contains("println!"), "a closed pipe is not a panic: {fb}");
        assert!(fb.contains("exit_line("), "the word the host reads comes from exit_line");
    }

    /// The guard: armed at the ask, overdue after the bound; it ends exactly the pids the state records (the node and
    /// every card's worker), each by pid, and the words name the stage.
    #[test]
    fn the_guard_ends_the_recorded_pids_and_names_the_stage() {
        assert_eq!(QUIT_BOUND_S, 45);
        assert!(NODE_GRACE_S <= 10 && UPLOAD_WAIT_S <= 10, "the normal path stays well inside the bound");
        let g = Guard::new(0.0);
        assert!(!g.overdue(QUIT_BOUND_S as f64 - 1.0));
        assert!(g.overdue(QUIT_BOUND_S as f64));
        let mut st = crate::state::State::default();
        st.node.pid = 4242;
        st.mining.cards.push(crate::state::CardState { pid: 777, ..Default::default() });
        st.mining.cards.push(crate::state::CardState { pid: 0, ..Default::default() });
        assert_eq!(recorded_pids(&st), vec![("node", 4242), ("worker", 777)]);
        assert_eq!(Stage::Miners.words(), "quitting: the miners are being stopped");
        assert_eq!(Stage::Node.words(), "quitting: the chain connection is being closed");
        assert_eq!(Stage::Logs.words(), "quitting: the last log upload");
        assert_eq!(Stage::Forced.words(), "quitting: the chain connection did not close in time; ending it by pid");
        if cfg!(windows) {
            return;
        }
        let mut child = std::process::Command::new("sleep").arg("100").spawn().unwrap(); // console: a unix-only test child (the test returns above on Windows)
        let pid = child.id();
        let lines = enforce_pids(&[("node", pid)]);
        assert_eq!(lines, vec![format!("QUIT event=forced what=node pid={pid}")]);
        // the child is reaped here (a killed but unreaped child still answers kill -0 as a zombie)
        let started = std::time::Instant::now();
        let mut ended = None;
        while ended.is_none() && started.elapsed() < std::time::Duration::from_secs(3) {
            ended = child.try_wait().unwrap();
            std::thread::sleep(std::time::Duration::from_millis(50));
        }
        assert!(ended.is_some(), "pid {pid} survived the guard");
        assert!(!pid_alive(pid), "pid {pid} is still a process after the reap");
    }
}
