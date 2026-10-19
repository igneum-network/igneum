/* The opening window size, one rule for both hosts (window-22, 7 October 2026, the founder on the second Windows machine: "why did the app open super
   wide?"). Inputs: the PRIMARY monitor's work area (never the virtual desktop spanning two monitors) and the remembered
   frame (0 when none). Width first: 80 percent of the work area, at most 1440 on a display up to 1920 wide and at most 1920
   beyond, whatever the display; height from the width at 16:10, bounded by the work area less 40 px; then the window is
   never wider than 1.6 times its height, never under 900 by 600 unless the work area itself is smaller, never over the work
   area. A remembered frame is kept only when it fits the work area, the minimum and the aspect cap, else discarded (a
   super-wide frame from an earlier host must not come back). Pure C99, no Windows headers: host.cpp includes it,
   opening_size_test.c compiles it on the gate, Ember.swift mirrors it line for line. */
#ifndef IGNEUM_OPENING_SIZE_H
#define IGNEUM_OPENING_SIZE_H
static void igneum_opening_size(int workW, int workH, int savedW, int savedH, int* outW, int* outH) {
    int minW = workW < 900 ? workW : 900, minH = workH < 600 ? workH : 600;
    int capW = workW <= 1920 ? 1440 : 1920;
    int w = (int)(workW * 0.8); if (w > capW) w = capW;
    int h = (int)(w / 1.6); if (h > workH - 40) h = workH - 40;
    if (w < minW) w = minW;
    if (h < minH) h = minH;
    if (w > (int)(h * 1.6)) w = (int)(h * 1.6);
    if (w > workW) w = workW;
    if (h > workH) h = workH;
    /* a remembered frame: the user's own size, kept when it fits; the aspect cap carries a 2 percent tolerance so a hand-sized
       1329 by 825 (the second Windows machine, 7 October 2026, aspect 1.611) is not thrown away for a few pixels */
    if (savedW >= 900 && savedH >= 600 && savedW <= workW && savedH <= workH && savedW <= (int)(savedH * 1.632)) { w = savedW; h = savedH; }
    *outW = w; *outH = h;
}
/* dpi-23 (7 October 2026): the same rule on a per-monitor-aware host, where the work area and the frame come in PHYSICAL
   pixels and the monitor's DPI is known (96 = 100 percent). The rule runs in logical pixels (physical x 96 / dpi), so the
   1440 by 900 and 1920 caps mean CSS pixels as they do on the Mac, and the result goes back to physical for CreateWindow.
   The remembered frame is logical on both the old host (a DPI-unaware process reads virtualised, logical coordinates) and
   this one (it saves physical / scale), so no stored value is thrown away for its units; the WindowUnits marker records which
   host wrote it. the second Windows machine: 5120 by 2112 physical at 192 DPI reads as 2560 by 1056 logical, opens 1625 by 1016 logical, 3250 by
   2032 physical, sharp. */
static int igneum_scale_to_logical(int px, int dpi) { return dpi > 0 ? (int)((long long)px * 96 / dpi) : px; }
static int igneum_scale_to_physical(int lg, int dpi) { return dpi > 0 ? (int)((long long)lg * dpi / 96) : lg; }
static void igneum_opening_size_scaled(int workWpx, int workHpx, int dpi, int savedWlogical, int savedHlogical, int* outWpx, int* outHpx, int* outWlogical, int* outHlogical) {
    int lw = 0, lh = 0;
    igneum_opening_size(igneum_scale_to_logical(workWpx, dpi), igneum_scale_to_logical(workHpx, dpi), savedWlogical, savedHlogical, &lw, &lh);
    if (outWlogical) *outWlogical = lw;
    if (outHlogical) *outHlogical = lh;
    *outWpx = igneum_scale_to_physical(lw, dpi); *outHpx = igneum_scale_to_physical(lh, dpi);
}
#endif
