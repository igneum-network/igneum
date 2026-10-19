# dmgbuild settings for Ember-<version>.dmg (used by build-dmg.sh; pip3 install dmgbuild).
# Defines passed in: app (the built .app), stage (the folder with README.txt and the Stop command), icons (brand/icons).
# Icon slots match brand/icons/make-icons.py (APP_SLOT, APPS_SLOT, ROW2_Y, ICON) and the arrow painted on the background.
import os
app = defines['app']
stage = defines['stage']
icons = defines['icons']
appname = os.path.basename(app)

format = 'ULFO'
filesystem = 'HFS+'
size = None
files = [app, os.path.join(stage, 'README.txt'), os.path.join(stage, 'Stop Ember.command')]
symlinks = {'Applications': '/Applications'}
icon = os.path.join(icons, 'igneum-volume.icns')   # the volume icon (.VolumeIcon.icns, hidden, custom-icon flag set): the rounded
                                                   # variant of the master mark; the app bundle itself carries the plain square igneum.icns
background = os.path.join(icons, 'dmg-background.tiff') if os.path.exists(os.path.join(icons, 'dmg-background.tiff')) else os.path.join(icons, 'dmg-background.png')
show_status_bar = False
show_tab_view = False
show_toolbar = False
show_pathbar = False
show_sidebar = False
sidebar_width = 180
window_rect = ((200, 120), (660, 400))
default_view = 'icon-view'
show_icon_preview = False
include_icon_view_settings = 'auto'
include_list_view_settings = 'auto'
arrange_by = None
grid_offset = (0, 0)
grid_spacing = 100
scroll_position = (0, 0)
label_pos = 'bottom'
text_size = 12
icon_size = 96
icon_locations = {
    appname: (165, 130),
    'Applications': (495, 130),
    # the two text files sit below the window's edge (the window is 660 x 400 with the title bar inside it; a second
    # row at 330 showed its labels clipped on the 14 October 2026 capture); a user who scrolls finds them
    'README.txt': (165, 560),
    'Stop Ember.command': (495, 560),
}
