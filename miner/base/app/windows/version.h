// The version of "Ember.exe" (the window host). One place for host.rc (the version block Explorer shows) and
// host.cpp (--version). Keep it equal to app/igneum-app/Cargo.toml and the AppVersion default in
// packaging/windows/Ember.iss when the app version moves. Include guards, not #pragma once: rc.exe reads it too.
#ifndef IGNEUM_HOST_VERSION_H
#define IGNEUM_HOST_VERSION_H
#define IGNEUM_HOST_VERSION_STR "0.2.8"
#define IGNEUM_HOST_VERSION_RC  0,2,4,0
// The WebView2 runtime the host's loader needs at least (the SDK 1.0.2903.40 loader's minimum, from its release note at the
// cut); the installer's rights step (src/rights.rs) installs the evergreen runtime when the PC's is absent or below it, and a
// raised minimum is a new right the next update asks once for.
#define IGNEUM_WEBVIEW2_MIN "109.0.1518.78"
#endif
