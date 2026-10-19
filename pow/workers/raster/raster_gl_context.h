// raster_gl_context.h: the OpenGL context (EGL surfaceless on Linux, a hidden window and WGL on Windows) and the GL 3.3 core entry points the
// raster probe and the version 18 raster worker share. GPU engineer B, 14 October 2026.
#pragma once
#include <cstdint>
#include <cstdio>
#include <chrono>
#include <cstdlib>
#include <string>
#include <vector>
#include <cstring>
#ifdef _WIN32
#include <windows.h>
#include <GL/gl.h>
#include <dxgi.h>
// ANGLE on Windows (the project's order 15 October 2026 16:0x UK): an EGL / GL ES 3.1 context over Direct3D 11 on the adapter NAMED, so a card that is
// not the primary display adapter (the Arc beside the 5090 in a desktop, an Ember miner's second card) runs the raster lane; libEGL.dll and
// libGLESv2.dll beside the worker (ANGLE's), the adapter by its DXGI LUID (EGL_ANGLE_platform_angle_d3d_luid). Every direct GL call goes
// through this table so the one text binds to opengl32 (WGL) or to ANGLE's libGLESv2 at run time.
struct RglCore {
    GLenum (APIENTRY *GetError)(); void (APIENTRY *ReadPixels)(GLint, GLint, GLsizei, GLsizei, GLenum, GLenum, void *); void (APIENTRY *Disable)(GLenum); void (APIENTRY *Enable)(GLenum); void (APIENTRY *Finish)();
    void (APIENTRY *BindTexture)(GLenum, GLuint); void (APIENTRY *TexSubImage2D)(GLenum, GLint, GLint, GLint, GLsizei, GLsizei, GLenum, GLenum, const void *); void (APIENTRY *TexParameteri)(GLenum, GLenum, GLint);
    const GLubyte *(APIENTRY *GetString)(GLenum); void (APIENTRY *TexImage2D)(GLenum, GLint, GLint, GLsizei, GLsizei, GLint, GLenum, GLenum, const void *); void (APIENTRY *PixelStorei)(GLenum, GLint);
    void (APIENTRY *GenTextures)(GLsizei, GLuint *); void (APIENTRY *DeleteTextures)(GLsizei, const GLuint *); void (APIENTRY *Clear)(GLbitfield); void (APIENTRY *ClearColor)(GLfloat, GLfloat, GLfloat, GLfloat);
    void (APIENTRY *Viewport)(GLint, GLint, GLsizei, GLsizei); void (APIENTRY *Scissor)(GLint, GLint, GLsizei, GLsizei); void (APIENTRY *DrawArrays)(GLenum, GLint, GLsizei); void (APIENTRY *BlendFunc)(GLenum, GLenum);
};
static RglCore rgl_core_;
static bool rgl_angle_ = false;   // the context is ANGLE's (GL ES): the ES shader dialect, no GL_MULTISAMPLE toggle
static HMODULE rgl_egl_ = nullptr, rgl_gles_ = nullptr;
#define glGetError rgl_core_.GetError
#define glReadPixels rgl_core_.ReadPixels
#define glDisable rgl_core_.Disable
#define glEnable rgl_core_.Enable
#define glFinish rgl_core_.Finish
#define glBindTexture rgl_core_.BindTexture
#define glTexSubImage2D rgl_core_.TexSubImage2D
#define glTexParameteri rgl_core_.TexParameteri
#define glGetString rgl_core_.GetString
#define glTexImage2D rgl_core_.TexImage2D
#define glPixelStorei rgl_core_.PixelStorei
#define glGenTextures rgl_core_.GenTextures
#define glDeleteTextures rgl_core_.DeleteTextures
#define glClear rgl_core_.Clear
#define glClearColor rgl_core_.ClearColor
#define glViewport rgl_core_.Viewport
#define glScissor rgl_core_.Scissor
#define glDrawArrays rgl_core_.DrawArrays
#define glBlendFunc rgl_core_.BlendFunc
#else
#include <EGL/egl.h>
#include <unistd.h>
#include <EGL/eglext.h>
#include <GL/gl.h>
#endif
#ifndef GL_ARRAY_BUFFER
#define GL_ARRAY_BUFFER 0x8892
#endif
#define GL_STATIC_DRAW_ 0x88E4
#define GL_PIXEL_PACK_BUFFER_ 0x88EB
#define GL_PIXEL_UNPACK_BUFFER_ 0x88EC
#define GL_STREAM_READ_ 0x88E1
#define GL_MAP_READ_BIT_ 0x0001
#define GL_COMPUTE_SHADER_ 0x91B9
#define GL_SHADER_STORAGE_BUFFER_ 0x90D2
#define GL_DYNAMIC_DRAW_ 0x88E8
#define GL_RGBA8UI_ 0x8D7C
#define GL_RGBA8_ 0x8058
#define GL_R32UI_ 0x8236
#define GL_RED_INTEGER_ 0x8D94
#define GL_READ_ONLY_ 0x88B8
#define GL_SHADER_IMAGE_ACCESS_BARRIER_BIT_ 0x00000020
#define GL_SHADER_STORAGE_BARRIER_BIT_ 0x00002000
#define GL_BUFFER_UPDATE_BARRIER_BIT_ 0x00000200
#define GL_FRAMEBUFFER_BARRIER_BIT_ 0x00000400
#define GL_TEXTURE_FETCH_BARRIER_BIT_ 0x00000008
#define GL_FRAGMENT_SHADER_ 0x8B30
#define GL_VERTEX_SHADER_ 0x8B31
#define GL_COMPILE_STATUS_ 0x8B81
#define GL_LINK_STATUS_ 0x8B82
#define GL_FRAMEBUFFER_ 0x8D40
#define GL_COLOR_ATTACHMENT0_ 0x8CE0
#define GL_FRAMEBUFFER_COMPLETE_ 0x8CD5
#define GL_FUNC_ADD_ 0x8006
#define GL_MULTISAMPLE_ 0x809D
#define GL_INFO_LOG_LENGTH_ 0x8B84
typedef char GLchar_; typedef ptrdiff_t GLsizeiptr_;
typedef GLuint (*PF_CreateShader)(GLenum); typedef void (*PF_ShaderSource)(GLuint, GLsizei, const GLchar_ *const *, const GLint *); typedef void (*PF_CompileShader)(GLuint);
typedef void (*PF_GetShaderiv)(GLuint, GLenum, GLint *); typedef void (*PF_GetShaderInfoLog)(GLuint, GLsizei, GLsizei *, GLchar_ *); typedef GLuint (*PF_CreateProgram)(void);
typedef void (*PF_AttachShader)(GLuint, GLuint); typedef void (*PF_LinkProgram)(GLuint); typedef void (*PF_GetProgramiv)(GLuint, GLenum, GLint *); typedef void (*PF_UseProgram)(GLuint);
typedef void (*PF_GenBuffers)(GLsizei, GLuint *); typedef void (*PF_BindBuffer)(GLenum, GLuint); typedef void (*PF_BufferData)(GLenum, GLsizeiptr_, const void *, GLenum);
typedef void *(*PF_MapBufferRange)(GLenum, ptrdiff_t, GLsizeiptr_, unsigned); typedef unsigned char (*PF_UnmapBuffer)(GLenum);
typedef void (*PF_DispatchCompute)(GLuint, GLuint, GLuint); typedef void (*PF_MemoryBarrier)(unsigned); typedef void (*PF_BindBufferBase)(GLenum, GLuint, GLuint);
typedef void (*PF_GetBufferSubData)(GLenum, ptrdiff_t, GLsizeiptr_, void *); typedef void (*PF_BindImageTexture)(GLuint, GLuint, GLint, unsigned char, GLint, GLenum, GLenum);
typedef void (*PF_TexStorage2D)(GLenum, GLsizei, GLenum, GLsizei, GLsizei); typedef void (*PF_BindBufferRange)(GLenum, GLuint, GLuint, ptrdiff_t, GLsizeiptr_); typedef void (*PF_Uniform1ui)(GLint, GLuint); typedef GLint (*PF_GetUniformLocation)(GLuint, const char *); typedef void (*PF_GetShaderInfoLog2)(GLuint, GLsizei, GLsizei *, char *);
typedef void (*PF_CopyImageSubData)(GLuint, GLenum, GLint, GLint, GLint, GLint, GLuint, GLenum, GLint, GLint, GLint, GLint, GLsizei, GLsizei, GLsizei);   // GL 4.3 / ARB_copy_image: the keyed restore on the card, no host upload
typedef void (*PF_GenVertexArrays)(GLsizei, GLuint *); typedef void (*PF_BindVertexArray)(GLuint); typedef void (*PF_EnableVertexAttribArray)(GLuint);
typedef void (*PF_VertexAttribPointer)(GLuint, GLint, GLenum, GLboolean, GLsizei, const void *); typedef void (*PF_GenFramebuffers)(GLsizei, GLuint *); typedef void (*PF_BindFramebuffer)(GLenum, GLuint);
typedef void (*PF_FramebufferTexture2D)(GLenum, GLenum, GLenum, GLuint, GLint); typedef GLenum (*PF_CheckFramebufferStatus)(GLenum); typedef void (*PF_BlendEquation)(GLenum); typedef void (*PF_BindAttribLocation)(GLuint, GLuint, const GLchar_ *);
static void *gl_proc(const char *n) {
#ifdef _WIN32
    if (rgl_angle_) { typedef void *(*PF_EGP)(const char *); static PF_EGP egp = rgl_egl_ ? (PF_EGP)GetProcAddress(rgl_egl_, "eglGetProcAddress") : nullptr; void *p = egp ? egp(n) : nullptr; if (!p && rgl_gles_) p = (void *)GetProcAddress(rgl_gles_, n); return p; }
    void *p = (void *)wglGetProcAddress(n); if (!p) { static HMODULE m = LoadLibraryA("opengl32.dll"); p = (void *)GetProcAddress(m, n); } return p;
#else
    return (void *)eglGetProcAddress(n);
#endif
}


// THE WORKER SPEAKS WHILE IT PREPARES (the product team's cell, 16 October 2026 02:5x UK: the Linux GL worker said nothing for 120 s on a cold first run
// and Ember's hello wait ended it; a line a stage moves Ember's deadline): every stage of the open and the pipeline prints one stderr line as it
// starts, with the milliseconds since the worker's start, so a host reads the stage a cold start sits in, never a silence
inline long long rgl_ms_since_start() { static const auto t0 = std::chrono::steady_clock::now(); return (long long)std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now() - t0).count(); }
inline void rgl_stage(const char *what, const char *stage, const std::string &detail = std::string()) { fprintf(stderr, "raster: %s: %s%s%s (%lld ms since start)\n", what, stage, detail.empty() ? "" : ": ", detail.c_str(), rgl_ms_since_start()); fflush(stderr); }
struct RasterGl {
    std::string device;
    PF_CreateShader CreateShader; PF_ShaderSource ShaderSource; PF_CompileShader CompileShader; PF_GetShaderiv GetShaderiv; PF_GetShaderInfoLog GetShaderInfoLog; PF_CreateProgram CreateProgram;
    PF_AttachShader AttachShader; PF_LinkProgram LinkProgram; PF_GetProgramiv GetProgramiv; PF_UseProgram UseProgram; PF_GenBuffers GenBuffers; PF_BindBuffer BindBuffer; PF_BufferData BufferData; PF_MapBufferRange MapBufferRange; PF_UnmapBuffer UnmapBuffer; PF_CopyImageSubData CopyImageSubData = nullptr; PF_DispatchCompute DispatchCompute = nullptr; PF_MemoryBarrier MemoryBarrier = nullptr; PF_BindBufferBase BindBufferBase = nullptr; PF_GetBufferSubData GetBufferSubData = nullptr; PF_BindImageTexture BindImageTexture = nullptr; PF_Uniform1ui Uniform1ui = nullptr; PF_GetUniformLocation GetUniformLocation = nullptr; PF_TexStorage2D TexStorage2D = nullptr; PF_BindBufferRange BindBufferRange = nullptr;
    PF_GenVertexArrays GenVertexArrays; PF_BindVertexArray BindVertexArray; PF_EnableVertexAttribArray EnableVertexAttribArray; PF_VertexAttribPointer VertexAttribPointer; PF_GenFramebuffers GenFramebuffers;
    PF_BindFramebuffer BindFramebuffer; PF_FramebufferTexture2D FramebufferTexture2D; PF_CheckFramebufferStatus CheckFramebufferStatus; PF_BlendEquation BlendEquation; PF_BindAttribLocation BindAttribLocation;
    // the context and the entry points; a refusal line on stdout and false when the platform has none
    // want: a substring of the renderer string that names the card (the rig's nine cards under one EGL: the device enumerated by
    // EGL_EXT_device_enumeration whose GL_RENDERER carries it is the one opened; empty = the first display that opens)
#ifdef _WIN32
    bool angle = false; void *angle_dpy = nullptr, *angle_ctx = nullptr, *angle_surf = nullptr;
    // the core table from opengl32 (WGL) or ANGLE's libGLESv2
    static bool load_core(HMODULE m) {
        #define RGL_LOAD(n) rgl_core_.n = (decltype(rgl_core_.n))GetProcAddress(m, "gl" #n); if (!rgl_core_.n) return false;
        RGL_LOAD(GetError) RGL_LOAD(ReadPixels) RGL_LOAD(Disable) RGL_LOAD(Enable) RGL_LOAD(Finish) RGL_LOAD(BindTexture) RGL_LOAD(TexSubImage2D) RGL_LOAD(TexParameteri) RGL_LOAD(GetString) RGL_LOAD(TexImage2D)
        RGL_LOAD(PixelStorei) RGL_LOAD(GenTextures) RGL_LOAD(DeleteTextures) RGL_LOAD(Clear) RGL_LOAD(ClearColor) RGL_LOAD(Viewport) RGL_LOAD(Scissor) RGL_LOAD(DrawArrays) RGL_LOAD(BlendFunc)
        #undef RGL_LOAD
        return true;
    }
    // ANGLE: libEGL.dll and libGLESv2.dll beside the worker; the adapter whose DXGI description carries `want` (or the first hardware adapter) by LUID; an ES 3.1 context on a 1 x 1 pbuffer
    bool open_angle(const char *what, const std::string &want, std::string &why) {
        char exe[MAX_PATH] = {0}; GetModuleFileNameA(nullptr, exe, MAX_PATH); std::string dir = exe; size_t sl = dir.find_last_of("\\/"); dir = sl == std::string::npos ? "." : dir.substr(0, sl);
        rgl_egl_ = LoadLibraryA((dir + "\\libEGL.dll").c_str()); rgl_gles_ = LoadLibraryA((dir + "\\libGLESv2.dll").c_str());
        if (!rgl_egl_ || !rgl_gles_) { why = "no libEGL.dll / libGLESv2.dll beside the worker"; return false; }
        typedef void *(*PF_GPD)(unsigned, void *, const int *); typedef unsigned (*PF_INIT)(void *, int *, int *); typedef unsigned (*PF_CC)(void *, const int *, void **, int, int *); typedef unsigned (*PF_BA)(unsigned);
        typedef void *(*PF_CTX)(void *, void *, void *, const int *); typedef void *(*PF_PB)(void *, void *, const int *); typedef unsigned (*PF_MC)(void *, void *, void *, void *); typedef int (*PF_GE)();
        PF_GPD gpd = (PF_GPD)GetProcAddress(rgl_egl_, "eglGetPlatformDisplayEXT"); PF_INIT init = (PF_INIT)GetProcAddress(rgl_egl_, "eglInitialize"); PF_CC cc = (PF_CC)GetProcAddress(rgl_egl_, "eglChooseConfig");
        PF_BA ba = (PF_BA)GetProcAddress(rgl_egl_, "eglBindAPI"); PF_CTX cctx = (PF_CTX)GetProcAddress(rgl_egl_, "eglCreateContext"); PF_PB pb = (PF_PB)GetProcAddress(rgl_egl_, "eglCreatePbufferSurface"); PF_MC mc = (PF_MC)GetProcAddress(rgl_egl_, "eglMakeCurrent"); PF_GE ge = (PF_GE)GetProcAddress(rgl_egl_, "eglGetError");
        if (!gpd || !init || !cc || !ba || !cctx || !pb || !mc) { why = "ANGLE's libEGL lacks an entry point"; return false; }
        // the adapter by DXGI
        LUID luid = {0, 0}; bool have = false; std::string seen;
        { typedef HRESULT (WINAPI *PF_CF)(REFIID, void **); HMODULE dx = LoadLibraryA("dxgi.dll"); PF_CF cf = dx ? (PF_CF)GetProcAddress(dx, "CreateDXGIFactory1") : nullptr; IDXGIFactory1 *f = nullptr;
          if (cf && SUCCEEDED(cf(__uuidof(IDXGIFactory1), (void **)&f)) && f) { IDXGIAdapter1 *a = nullptr; for (UINT i = 0; f->EnumAdapters1(i, &a) != DXGI_ERROR_NOT_FOUND; i++) { DXGI_ADAPTER_DESC1 d; a->GetDesc1(&d); char nm[256] = {0}; for (int k = 0; k < 127 && d.Description[k]; k++) nm[k] = (char)(d.Description[k] < 128 ? d.Description[k] : '?');
              const bool sw = (d.Flags & DXGI_ADAPTER_FLAG_SOFTWARE) != 0; seen += (seen.empty() ? "" : "; ") + std::string(nm); if (!have && !sw && (want.empty() || std::string(nm).find(want) != std::string::npos)) { luid = d.AdapterLuid; have = true; } a->Release(); } f->Release(); } }
        if (!have) { why = "no DXGI adapter named " + want + " (seen: " + seen + ")"; return false; }
        const int dattr[] = { 0x3203 /* EGL_PLATFORM_ANGLE_TYPE_ANGLE */, 0x3208 /* D3D11 */, 0x34A0 /* LUID_HIGH */, (int)luid.HighPart, 0x34A1 /* LUID_LOW */, (int)luid.LowPart, 0x3038 /* EGL_NONE */ };
        void *dpy = gpd(0x3202 /* EGL_PLATFORM_ANGLE_ANGLE */, (void *)0 /* EGL_DEFAULT_DISPLAY */, dattr);
        int maj = 0, mnr = 0; if (!dpy || !init(dpy, &maj, &mnr)) { why = "ANGLE's D3D11 display on the adapter did not initialise (eglGetError " + std::to_string(ge ? ge() : 0) + ")"; return false; }
        const int cfg_attr[] = { 0x3033 /* EGL_SURFACE_TYPE */, 0x0001 /* EGL_PBUFFER_BIT */, 0x3040 /* EGL_RENDERABLE_TYPE */, 0x0040 /* EGL_OPENGL_ES3_BIT */, 0x3024, 8, 0x3023, 8, 0x3022, 8, 0x3021, 8, 0x3038 };
        void *cfg = nullptr; int ncfg = 0; cc(dpy, cfg_attr, &cfg, 1, &ncfg); if (ncfg < 1) { why = "no ES3 RGBA8 pbuffer config on ANGLE"; return false; }
        ba(0x30A0 /* EGL_OPENGL_ES_API */);
        const int ctx_attr[] = { 0x3098 /* EGL_CONTEXT_MAJOR_VERSION */, 3, 0x30FB /* EGL_CONTEXT_MINOR_VERSION */, 1, 0x3038 };
        void *ctx = cctx(dpy, cfg, (void *)0, ctx_attr); if (!ctx) { why = "no ES 3.1 context on ANGLE (eglGetError " + std::to_string(ge ? ge() : 0) + ")"; return false; }
        const int pb_attr[] = { 0x3057 /* EGL_WIDTH */, 1, 0x3056 /* EGL_HEIGHT */, 1, 0x3038 };
        void *surf = pb(dpy, cfg, pb_attr); if (!surf) { why = "no pbuffer on ANGLE"; return false; }
        if (!mc(dpy, surf, surf, ctx)) { why = "eglMakeCurrent refused on ANGLE"; return false; }
        rgl_angle_ = true; angle = true; angle_dpy = dpy; angle_ctx = ctx; angle_surf = surf;
        if (!load_core(rgl_gles_)) { why = "ANGLE's libGLESv2 lacks a core entry point"; return false; }
        (void)what; return true;
    }
    // the context bound to the calling thread (the protocol loop runs the job on its own thread): WGL or ANGLE
    HDC wdc_ = nullptr; HGLRC wrc_ = nullptr;
    bool make_current() { if (angle) { typedef unsigned (*PF_MC)(void *, void *, void *, void *); PF_MC mc = (PF_MC)GetProcAddress(rgl_egl_, "eglMakeCurrent"); return mc && mc(angle_dpy, angle_surf, angle_surf, angle_ctx); } return wrc_ && wglMakeCurrent(wdc_, wrc_) == TRUE; }
    void release_current() { if (angle) { typedef unsigned (*PF_MC)(void *, void *, void *, void *); PF_MC mc = (PF_MC)GetProcAddress(rgl_egl_, "eglMakeCurrent"); if (mc) mc(angle_dpy, nullptr, nullptr, nullptr); } else wglMakeCurrent(nullptr, nullptr); }
#else
    EGLDisplay edpy_ = EGL_NO_DISPLAY; EGLContext ectx_ = EGL_NO_CONTEXT;
    bool make_current() { return ectx_ != EGL_NO_CONTEXT && eglMakeCurrent(edpy_, EGL_NO_SURFACE, EGL_NO_SURFACE, ectx_) == EGL_TRUE; }
    void release_current() { if (edpy_ != EGL_NO_DISPLAY) eglMakeCurrent(edpy_, EGL_NO_SURFACE, EGL_NO_SURFACE, EGL_NO_CONTEXT); }
#endif
    bool open(const char *what, const std::string &want = std::string()) {
    device = "?";   // the member (a local of the same name shadowed it until 15 October 2026: every bench line read device "")
#ifdef _WIN32
    bool via_angle = false;
    { std::string why; if (open_angle(what, want, why)) { const char *r = (const char *)glGetString(GL_RENDERER), *v = (const char *)glGetString(GL_VERSION); device = std::string(r ? r : "?") + " (" + (v ? v : "?") + ")"; if (!want.empty() && device.find(want) == std::string::npos) { printf("{\"%s\": {\"backend\": \"opengl\", \"refused\": \"ANGLE opened %s, not the card named %s\"}}\n", what, device.c_str(), want.c_str()); return false; } via_angle = true; }
      else { rgl_angle_ = false; if (!want.empty() && rgl_egl_) fprintf(stderr, "raster: ANGLE beside the worker but no context on the named card (%s); WGL on the primary adapter next\n", why.c_str()); } }
    if (!via_angle) {
    if (!load_core(LoadLibraryA("opengl32.dll"))) { printf("{\"%s\": {\"backend\": \"opengl\", \"refused\": \"opengl32.dll lacks a core entry point\"}}\n", what); return false; }
    WNDCLASSA wc = {}; wc.lpfnWndProc = DefWindowProcA; wc.hInstance = GetModuleHandleA(nullptr); wc.lpszClassName = "igneum_raster_probe"; wc.style = CS_OWNDC; RegisterClassA(&wc);
    HWND hw = CreateWindowA(wc.lpszClassName, "igneum raster probe", WS_OVERLAPPEDWINDOW, 0, 0, 64, 64, nullptr, nullptr, wc.hInstance, nullptr);
    HDC dc = GetDC(hw); PIXELFORMATDESCRIPTOR pfd = {}; pfd.nSize = sizeof pfd; pfd.nVersion = 1; pfd.dwFlags = PFD_DRAW_TO_WINDOW | PFD_SUPPORT_OPENGL; pfd.iPixelType = PFD_TYPE_RGBA; pfd.cColorBits = 32;
    int pf = ChoosePixelFormat(dc, &pfd); if (!pf || !SetPixelFormat(dc, pf, &pfd)) { printf("{\"%s\": {\"backend\": \"opengl\", \"refused\": \"no pixel format\"}}\n", what); return false; }
    HGLRC rc = wglCreateContext(dc); if (!rc || !wglMakeCurrent(dc, rc)) { printf("{\"%s\": {\"backend\": \"opengl\", \"refused\": \"no WGL context\"}}\n", what); return false; }
    {   // a 3.3 core context through wglCreateContextAttribsARB when the driver offers it
        typedef HGLRC (WINAPI *PF_CCA)(HDC, HGLRC, const int *); PF_CCA cca = (PF_CCA)wglGetProcAddress("wglCreateContextAttribsARB");
        if (cca) { const int attr[] = {0x2091, 3, 0x2092, 3, 0x9126, 0x00000001, 0}; HGLRC rc2 = cca(dc, nullptr, attr); if (rc2) { wglMakeCurrent(dc, rc2); wglDeleteContext(rc); rc = rc2; } }
    }
    wdc_ = dc; wrc_ = rc;
    }
#else
    rgl_stage(what, "enumerating the EGL devices", want.empty() ? "" : "the card named " + want);
    typedef EGLDisplay (*PF_GPD)(EGLenum, void *, const EGLint *); PF_GPD gpd = (PF_GPD)eglGetProcAddress("eglGetPlatformDisplayEXT");
    typedef EGLBoolean (*PF_QD)(EGLint, void **, EGLint *); PF_QD qdev = (PF_QD)eglGetProcAddress("eglQueryDevicesEXT");
    // the candidates: every EGL device (EGL_PLATFORM_DEVICE_EXT, 0x313F) when the extension exists, then the surfaceless Mesa display, then the default display
    std::vector<void *> devs; if (qdev && gpd) { EGLint n = 0; if (qdev(0, nullptr, &n) && n > 0) { devs.resize((size_t)n); if (!qdev(n, devs.data(), &n)) n = 0; devs.resize((size_t)(n > 0 ? n : 0)); } }
    const EGLint cfg_attr[] = { EGL_SURFACE_TYPE, EGL_PBUFFER_BIT, EGL_RENDERABLE_TYPE, EGL_OPENGL_BIT, EGL_RED_SIZE, 8, EGL_GREEN_SIZE, 8, EGL_BLUE_SIZE, 8, EGL_ALPHA_SIZE, 8, EGL_NONE };
    const EGLint ctx_attr[] = { EGL_CONTEXT_MAJOR_VERSION, 3, EGL_CONTEXT_MINOR_VERSION, 3, EGL_CONTEXT_OPENGL_PROFILE_MASK, EGL_CONTEXT_OPENGL_CORE_PROFILE_BIT, EGL_NONE };
    bool opened = false; std::string seen; rgl_stage(what, "EGL devices enumerated", std::to_string(devs.size()) + " device(s)");
    auto try_display = [&](EGLDisplay dpy) -> bool {
        rgl_stage(what, "initialising an EGL display and opening a GL 3.3 core context on it");
        EGLint maj = 0, mnr = 0; if (dpy == EGL_NO_DISPLAY || !eglInitialize(dpy, &maj, &mnr)) return false;
        EGLConfig cfg; EGLint ncfg = 0; eglChooseConfig(dpy, cfg_attr, &cfg, 1, &ncfg);
        eglBindAPI(EGL_OPENGL_API);
        EGLContext ctx = eglCreateContext(dpy, ncfg ? cfg : nullptr, EGL_NO_CONTEXT, ctx_attr);
        if (ctx == EGL_NO_CONTEXT || !eglMakeCurrent(dpy, EGL_NO_SURFACE, EGL_NO_SURFACE, ctx)) { if (ctx != EGL_NO_CONTEXT) eglDestroyContext(dpy, ctx); eglTerminate(dpy); return false; }
        const char *r = (const char *)glGetString(GL_RENDERER), *v = (const char *)glGetString(GL_VERSION); const std::string rs = r ? r : "?";
        if (!want.empty() && rs.find(want) == std::string::npos) { seen += (seen.empty() ? "" : "; ") + rs; eglMakeCurrent(dpy, EGL_NO_SURFACE, EGL_NO_SURFACE, EGL_NO_CONTEXT); eglDestroyContext(dpy, ctx); eglTerminate(dpy); return false; }
        device = rs + " (" + (v ? v : "?") + ")"; edpy_ = dpy; ectx_ = ctx; rgl_stage(what, "the context is open", device); return true;
    };
    // THE HELD CARD (reliability's agent 0.3.74, 15 October 2026: IGNEUM_CARD_BUS names the job's card by PCI bus and a brace ends a step that
    // holds another card's node): when the bus is set, the one EGL device whose DRM node sits on that bus is opened and no other is touched
    // (EGL_EXT_device_drm / _render_node strings need no display; the node's bus from sysfs); without it, the walk by renderer name as before
    typedef const char *(*PF_QDS)(void *, EGLint); PF_QDS qds = (PF_QDS)eglGetProcAddress("eglQueryDeviceStringEXT");
    // the bus in one form: the domain as a number and bus:device.function as written (the agent writes NVML's eight-digit domain "00000000:05:00.0", sysfs "0000:05:00.0")
    // 16 October 2026 22:1x UK (a 4090 at bus 0a:00.0): NVML writes the hex letters upper-case ("00000000:0A:00.0"), sysfs lower-case
    // ("0000:0a:00.0"); both sides lower-cased before the compare (the agent's card brace had the same fault, cured in 0.3.91)
    auto norm_bus = [](std::string b) -> std::string { for (auto &ch : b) ch = (char)tolower((unsigned char)ch); if (b.rfind("pci-", 0) == 0) b = b.substr(4); const size_t c = b.find(':'); if (c == std::string::npos) return b; std::string dom = b.substr(0, c), rest = b.substr(c + 1); size_t z = 0; while (z + 1 < dom.size() && dom[z] == '0') z++; return dom.substr(z) + ":" + rest; };
    std::string held_bus; if (const char *hb = getenv("IGNEUM_CARD_BUS")) held_bus = norm_bus(hb);
    auto bus_of_node = [](const std::string &node) -> std::string {   // /dev/dri/renderD136 or /dev/dri/card3 -> 0000:11:00.0 through /sys/class/drm/<name>/device
        const size_t sl = node.find_last_of('/'); const std::string name = sl == std::string::npos ? node : node.substr(sl + 1); if (name.empty()) return "";
        char buf[512]; const std::string link = "/sys/class/drm/" + name + "/device"; const ssize_t n = readlink(link.c_str(), buf, sizeof buf - 1); if (n <= 0) return "";
        buf[n] = 0; const std::string t(buf); const size_t s2 = t.find_last_of('/'); return s2 == std::string::npos ? t : t.substr(s2 + 1); };
    if (!held_bus.empty() && qds) {
        for (void *d : devs) {
            const char *rn = qds(d, 0x3377 /* EGL_DRM_RENDER_NODE_FILE_EXT */); const char *df = qds(d, 0x3233 /* EGL_DRM_DEVICE_FILE_EXT */);
            std::string bus = rn ? bus_of_node(rn) : ""; if (bus.empty() && df) bus = bus_of_node(df); if (!bus.empty()) bus = norm_bus(bus);
            if (!bus.empty() && bus == held_bus) { if (try_display(gpd(0x313F, d, nullptr))) { opened = true; } break; }
        }
        if (!opened) { printf("{\"%s\": {\"backend\": \"opengl\", \"refused\": \"no EGL device on the held card's bus %s%s%s (%zu devices enumerated%s%s)\"}}\n", what, held_bus.c_str(), want.empty() ? "" : " named ", want.c_str(), devs.size(), seen.empty() ? "" : "; the renderers seen: ", seen.c_str()); return false; }
    }
    for (void *d : devs) { if (opened) break; if (try_display(gpd(0x313F /* EGL_PLATFORM_DEVICE_EXT */, d, nullptr))) { opened = true; break; } }
    if (!opened && gpd) opened = try_display(gpd(0x31DD /* EGL_PLATFORM_SURFACELESS_MESA */, EGL_DEFAULT_DISPLAY, nullptr));
    if (!opened) opened = try_display(eglGetDisplay(EGL_DEFAULT_DISPLAY));
    if (!opened) { printf("{\"%s\": {\"backend\": \"opengl\", \"refused\": \"no GL 3.3 core context on EGL%s%s (%zu devices enumerated%s%s)\"}}\n", what, want.empty() ? "" : " for the card named ", want.c_str(), devs.size(), seen.empty() ? "" : "; the renderers seen: ", seen.c_str()); return false; }
#endif
#ifdef _WIN32
    if (!via_angle) { const char *r = (const char *)glGetString(GL_RENDERER), *v = (const char *)glGetString(GL_VERSION); device = std::string(r ? r : "?") + " (" + (v ? v : "?") + ")"; if (!want.empty() && device.find(want) == std::string::npos) { printf("{\"%s\": {\"backend\": \"opengl\", \"refused\": \"the WGL context is on %s, not the card named %s (ANGLE's libEGL.dll and libGLESv2.dll beside the worker would open the named adapter over D3D11)\"}}\n", what, device.c_str(), want.c_str()); return false; } }
#endif

    rgl_stage(what, "loading the GL entry points");
#define LOADF(T, n) n = (T)gl_proc("gl" #n); if (!n) { printf("{\"%s\": {\"backend\": \"opengl\", \"refused\": \"gl" #n " missing\"}}\n", what); return false; }
        LOADF(PF_CreateShader, CreateShader) LOADF(PF_ShaderSource, ShaderSource) LOADF(PF_CompileShader, CompileShader) LOADF(PF_GetShaderiv, GetShaderiv) LOADF(PF_GetShaderInfoLog, GetShaderInfoLog)
        LOADF(PF_CreateProgram, CreateProgram) LOADF(PF_AttachShader, AttachShader) LOADF(PF_LinkProgram, LinkProgram) LOADF(PF_GetProgramiv, GetProgramiv) LOADF(PF_UseProgram, UseProgram)
        LOADF(PF_GenBuffers, GenBuffers) LOADF(PF_BindBuffer, BindBuffer) LOADF(PF_BufferData, BufferData) LOADF(PF_MapBufferRange, MapBufferRange) LOADF(PF_UnmapBuffer, UnmapBuffer) LOADF(PF_GenVertexArrays, GenVertexArrays) LOADF(PF_BindVertexArray, BindVertexArray)
        LOADF(PF_EnableVertexAttribArray, EnableVertexAttribArray) LOADF(PF_VertexAttribPointer, VertexAttribPointer) LOADF(PF_GenFramebuffers, GenFramebuffers) LOADF(PF_BindFramebuffer, BindFramebuffer)
        LOADF(PF_FramebufferTexture2D, FramebufferTexture2D) LOADF(PF_CheckFramebufferStatus, CheckFramebufferStatus) LOADF(PF_BlendEquation, BlendEquation) LOADF(PF_BindAttribLocation, BindAttribLocation)
#undef LOADF
        TexStorage2D = (PF_TexStorage2D)gl_proc("glTexStorage2D"); BindBufferRange = (PF_BindBufferRange)gl_proc("glBindBufferRange");   // ES 3.0 and GL 4.2: an immutable texture, which ES 3.1 requires of a texture bound as an image (the digest pass read GL_INVALID_OPERATION on ANGLE until the target was immutable, 15 October 2026 16:2x UK)
        CopyImageSubData = nullptr;   // optional: absent on a GL below 4.3, the restore then uploads from the host copy; under ES it is EXT_copy_image's entry point when the extension is listed (ANGLE answers every name, so the list decides)
        if (is_es()) { const char *ext = (const char *)glGetString(GL_EXTENSIONS); if (ext && strstr(ext, "GL_EXT_copy_image")) CopyImageSubData = (PF_CopyImageSubData)gl_proc("glCopyImageSubDataEXT"); }
        else CopyImageSubData = (PF_CopyImageSubData)gl_proc("glCopyImageSubData");
        // optional too (GL 4.3 compute): the boxes digested on the card; absent, the boxes come back through the pack buffer
        if (is_es()) { const char *v = (const char *)glGetString(GL_VERSION); if (!v || !strstr(v, "OpenGL ES 3.") || (strstr(v, "OpenGL ES 3.0"))) { return true; } }   // ES 3.0: no compute, no image load; the pack-buffer path
        DispatchCompute = (PF_DispatchCompute)gl_proc("glDispatchCompute"); MemoryBarrier = (PF_MemoryBarrier)gl_proc("glMemoryBarrier"); BindBufferBase = (PF_BindBufferBase)gl_proc("glBindBufferBase");
        GetBufferSubData = (PF_GetBufferSubData)gl_proc("glGetBufferSubData"); BindImageTexture = (PF_BindImageTexture)gl_proc("glBindImageTexture"); Uniform1ui = (PF_Uniform1ui)gl_proc("glUniform1ui"); GetUniformLocation = (PF_GetUniformLocation)gl_proc("glGetUniformLocation");
        return true;
    }
    // the target's storage: immutable (glTexStorage2D) under ES, where an image binding needs it; glTexImage2D on desktop GL as before
    void make_target_storage(GLsizei side_px) { if (is_es() && TexStorage2D) TexStorage2D(GL_TEXTURE_2D, 1, GL_RGBA8_, side_px, side_px); else glTexImage2D(GL_TEXTURE_2D, 0, GL_RGBA8, side_px, side_px, 0, GL_RGBA, GL_UNSIGNED_BYTE, nullptr); }
    bool is_es() const {
#ifdef _WIN32
        return angle;
#else
        return false;
#endif
    }
    bool has_compute() const { return DispatchCompute && MemoryBarrier && BindBufferBase && BindBufferRange && BindImageTexture && Uniform1ui && GetUniformLocation && MapBufferRange && UnmapBuffer; }   // the chunk values come back through MapBufferRange (GL ES has no GetBufferSubData)
    // a compute program from one source; 0 and the log's head on stderr when the driver refuses it
    GLuint compute_program(const char *src) {
        if (!has_compute()) return 0;
        GLuint sh = CreateShader(GL_COMPUTE_SHADER_); if (!sh) { glGetError(); return 0; }
        ShaderSource(sh, 1, &src, nullptr); CompileShader(sh); GLint ok = 0; GetShaderiv(sh, GL_COMPILE_STATUS_, &ok);
        if (!ok) { char log[2048] = {0}; GetShaderInfoLog(sh, sizeof log - 1, nullptr, log); fprintf(stderr, "raster: the digest compute shader was refused: %.600s\n", log); return 0; }
        GLuint pr = CreateProgram(); AttachShader(pr, sh); LinkProgram(pr); GetProgramiv(pr, GL_LINK_STATUS_, &ok);
        if (!ok) { fprintf(stderr, "raster: the digest compute program did not link\n"); return 0; }
        return pr;
    }
    // the pipeline: the shaders, the 8192 x 8192 RGBA8 target on an FBO, the blend state; false on a refusal
    GLuint prog = 0, tex = 0, fbo = 0, vao = 0, vbo = 0;
    uint32_t side = 8192;   // the target's side: 8192 (the first rule) or 16384 (18b/18c)
    bool pipeline(const char *what, uint32_t side_ = 8192) {
        side = side_;
        const bool es = is_es(); rgl_stage(what, "compiling the draw shaders");
        const char *vs = es ? "#version 300 es\nlayout(location = 0) in vec2 p; layout(location = 1) in vec4 c; out highp vec4 vc; void main() { gl_Position = vec4(p, 0.0, 1.0); vc = c; }\n"
                            : "#version 330 core\nlayout(location = 0) in vec2 p; layout(location = 1) in vec4 c; out vec4 vc; void main() { gl_Position = vec4(p, 0.0, 1.0); vc = c; }\n";
        const char *fs = es ? "#version 300 es\nprecision highp float; in highp vec4 vc; out highp vec4 o; void main() { o = vc; }\n"
                            : "#version 330 core\nin vec4 vc; out vec4 o; void main() { o = vc; }\n";
        GLuint sv = CreateShader(GL_VERTEX_SHADER_), sf = CreateShader(GL_FRAGMENT_SHADER_); ShaderSource(sv, 1, &vs, nullptr); CompileShader(sv); ShaderSource(sf, 1, &fs, nullptr); CompileShader(sf);
        GLint ok = 0, ok2 = 0; GetShaderiv(sv, GL_COMPILE_STATUS_, &ok); GetShaderiv(sf, GL_COMPILE_STATUS_, &ok2);
        if (!ok || !ok2) { printf("{\"%s\": {\"backend\": \"opengl\", \"refused\": \"shader\"}}\n", what); return false; }
        prog = CreateProgram(); AttachShader(prog, sv); AttachShader(prog, sf); BindAttribLocation(prog, 0, "p"); BindAttribLocation(prog, 1, "c"); LinkProgram(prog); GetProgramiv(prog, GL_LINK_STATUS_, &ok);
        if (!ok) { printf("{\"%s\": {\"backend\": \"opengl\", \"refused\": \"link\"}}\n", what); return false; }
        rgl_stage(what, "allocating the target", std::to_string(side) + " x " + std::to_string(side) + " RGBA8");
        glGenTextures(1, &tex); glBindTexture(GL_TEXTURE_2D, tex); make_target_storage((GLsizei)side);
        glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, GL_NEAREST); glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, GL_NEAREST);
        GenFramebuffers(1, &fbo); BindFramebuffer(GL_FRAMEBUFFER_, fbo); FramebufferTexture2D(GL_FRAMEBUFFER_, GL_COLOR_ATTACHMENT0_, GL_TEXTURE_2D, tex, 0);
        if (CheckFramebufferStatus(GL_FRAMEBUFFER_) != GL_FRAMEBUFFER_COMPLETE_ || glGetError() != GL_NO_ERROR) { printf("{\"%s\": {\"backend\": \"opengl\", \"device\": \"%s\", \"refused\": \"the %u x %u RGBA8 framebuffer is not complete\"}}\n", what, device.c_str(), side, side); return false; }
        GenVertexArrays(1, &vao); BindVertexArray(vao); GenBuffers(1, &vbo); BindBuffer(GL_ARRAY_BUFFER, vbo);
        EnableVertexAttribArray(0); VertexAttribPointer(0, 2, GL_FLOAT, GL_FALSE, 12, (const void *)0); EnableVertexAttribArray(1); VertexAttribPointer(1, 4, GL_UNSIGNED_BYTE, GL_TRUE, 12, (const void *)8);
        glViewport(0, 0, (GLsizei)side, (GLsizei)side); glDisable(GL_DEPTH_TEST); glDisable(GL_CULL_FACE); glDisable(GL_SCISSOR_TEST); if (!es) glDisable(GL_MULTISAMPLE_); glDisable(GL_DITHER);
        glEnable(GL_BLEND); glBlendFunc(GL_ONE, GL_ONE); BlendEquation(GL_FUNC_ADD_);
        glClearColor(0, 0, 0, 0); glClear(GL_COLOR_BUFFER_BIT); UseProgram(prog); BindVertexArray(vao); glPixelStorei(GL_PACK_ALIGNMENT, 1); glPixelStorei(GL_UNPACK_ALIGNMENT, 1);
        glFinish(); rgl_stage(what, "the pipeline is ready", "the target cleared");
        return true;
    }
};
