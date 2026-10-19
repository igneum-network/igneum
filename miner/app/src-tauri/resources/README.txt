The workers this build carries go here, with workers.sha256 (one "<sha256>  <file name>" per line). The supervisor
launches only igneum-worker-* files named in that manifest with a matching hash (design section 1.2).
scripts/stage-workers.sh fills this directory from the release build. This file keeps the directory in the
tree so the bundler's resources/* glob matches before the stage script has run.
