-- finder-layout.applescript <volume path> <background file name>: Finder itself writes the disk image window's layout
-- (F-E42, 14 October 2026: macOS 26's Finder ignores dmgbuild's alias record for the background, so the window opened
-- plain; a .DS_Store Finder wrote paints on every macOS). The window 660 x 400, icon view, 96 px icons, labels below,
-- no toolbar, status bar, path bar or sidebar, the app at (165, 130), Applications at (495, 130), the two text files
-- below the edge, the background the hidden file on the volume; the window closed so the state is saved.
on run argv
  set vol to item 1 of argv
  set bgName to item 2 of argv
  tell application "Finder"
    set d to (POSIX file vol) as alias
    open d
    delay 1
    set w to container window of d
    set current view of w to icon view
    set toolbar visible of w to false
    set statusbar visible of w to false
    set sidebar width of w to 0
    set bounds of w to {200, 120, 860, 520}
    set o to icon view options of w
    set arrangement of o to not arranged
    set icon size of o to 96
    set text size of o to 12
    set label position of o to bottom
    set shows item info of o to false
    set shows icon preview of o to false
    set background picture of o to file bgName of d
    set position of item "Ember.app" of d to {165, 130}
    set position of item "Applications" of d to {495, 130}
    try
      set position of item "README.txt" of d to {165, 560}
    end try
    try
      set position of item "Stop Ember.command" of d to {495, 560}
    end try
    update d without registering applications
    delay 2
    close w
  end tell
end run
