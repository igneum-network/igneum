/* The opening window size (window-22, 7 October 2026, the founder on the second Windows machine: "why did the app open super wide?"): the rule in
   opening_size.h, shared by the Windows host (host.cpp) and mirrored in the Mac window (Ember.swift). Known-failed
   first: on 0.1.21's rule a 3440 by 1440 work area opened 2752 by 1152 and a 3840 by 1080 span opened 3072 by 864
   (the test could not compile before the header existed).
   cc -std=c99 -Wall -Wextra -o t app/windows/opening_size_test.c && ./t */
#include <stdio.h>
#include "opening_size.h"
static int fails = 0;
static void checkS(const char* name, int ww, int wh, int dpi, int sw, int sh, int ew, int eh, int elw, int elh) {
    int w = 0, h = 0, lw = 0, lh = 0; igneum_opening_size_scaled(ww, wh, dpi, sw, sh, &w, &h, &lw, &lh);
    if (w != ew || h != eh || lw != elw || lh != elh) { printf("FAIL %s: work %dx%d @%d dpi saved %dx%d -> %dx%d px (%dx%d logical), wanted %dx%d px (%dx%d)\n", name, ww, wh, dpi, sw, sh, w, h, lw, lh, ew, eh, elw, elh); fails++; }
    else printf("ok   %s: %dx%d px, %dx%d logical\n", name, w, h, lw, lh);
}
static void check(const char* name, int ww, int wh, int sw, int sh, int ew, int eh) {
    int w = 0, h = 0; igneum_opening_size(ww, wh, sw, sh, &w, &h);
    if (w != ew || h != eh) { printf("FAIL %s: work %dx%d saved %dx%d -> %dx%d, wanted %dx%d\n", name, ww, wh, sw, sh, w, h, ew, eh); fails++; }
    else printf("ok   %s: %dx%d\n", name, w, h);
}
int main(void) {
    check("1080p desk: 1440 by 900", 1920, 1040, 0, 0, 1440, 900);
    check("1440p monitor: width capped at 1920, 16:10", 2560, 1400, 0, 0, 1920, 1200);
    check("4K: width capped at 1920 regardless of display", 3840, 2120, 0, 0, 1920, 1200);
    check("ultrawide 3440 by 1440: never wider than 1.6 times the height", 3440, 1400, 0, 0, 1920, 1200);
    check("two monitors spanned 3840 by 1080, if ever read as one area: the aspect cap holds", 3840, 1040, 0, 0, 1600, 1000);
    check("a remembered frame that fits is kept", 1920, 1040, 1400, 900, 1400, 900);
    check("the second Windows machine, 7 October 2026: a 5120 by 2160 panel at 200 percent reads as 2560 by 1032; 0.1.21 opened 1920 by 826", 2560, 1032, 0, 0, 1587, 992);
    check("the second Windows machine: the hand-sized 1329 by 825 (aspect 1.611) is kept under the 2 percent tolerance", 2560, 1032, 1329, 825, 1329, 825);
    check("a remembered frame at aspect 1.7 is discarded", 2560, 1032, 1700, 1000, 1587, 992);
    check("a remembered super-wide frame from the take-4 host is discarded", 1920, 1040, 2752, 1152, 1440, 900);
    check("a remembered frame wider than 1.6 times its height is discarded", 1920, 1040, 1800, 700, 1440, 900);
    check("a remembered frame under the minimum is discarded", 1920, 1040, 800, 500, 1440, 900);
    check("a small laptop: 80 percent wide, 16:10", 1280, 760, 0, 0, 1024, 640);
    check("a tiny work area: the work area itself", 800, 560, 0, 0, 800, 560);
    /* dpi-23: the per-monitor-aware host reads physical pixels and the monitor's DPI */
    checkS("1080p at 100 percent: unchanged", 1920, 1040, 96, 0, 0, 1440, 900, 1440, 900);
    checkS("the second Windows machine: 5120 by 2112 physical at 200 percent (2560 by 1056 logical)", 5120, 2112, 192, 0, 0, 3250, 2032, 1625, 1016);
    checkS("4K at 150 percent: 1920 by 1200 logical, 2880 by 1800 physical", 3840, 2120, 144, 0, 0, 2880, 1800, 1920, 1200);
    checkS("a 1440p panel at 125 percent (2048 by 1120 logical)", 2560, 1400, 120, 0, 0, 2045, 1278, 1636, 1023);
    checkS("the second Windows machine's remembered 1329 by 825 (logical, from the unaware host) is kept and placed at 200 percent", 5120, 2112, 192, 1329, 825, 2658, 1650, 1329, 825);
    checkS("a 250 percent laptop panel (1536 by 832 logical): 80 percent wide at 16:10", 3840, 2080, 240, 0, 0, 3067, 1917, 1227, 767);
    printf(fails ? "%d FAILED\n" : "all ok\n", fails); return fails ? 1 : 0;
}
