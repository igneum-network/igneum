// igneum-raster-probe: the raster determinism probe through OpenGL (raster_probe.h holds the shared rule; the Metal form is the Metal
// worker's `raster` verb, the CUDA/Vulkan form the GPU team's). Linux: EGL on the surfaceless platform (no display), GL 3.3 core; Windows: a
// hidden window and WGL, GL 3.3 core. The framebuffer is an 8192 x 8192 RGBA8 texture on an FBO; the triangles one draw of 3,000,000
// vertices (position as the NDC float2 of the integer corner, the colour as four normalised bytes); blend ONE + ONE ADD; the readback
// by glReadPixels with the rows reversed (GL's lower-left origin against the probe's top-left); then the cost loop: the same draw
// repeated for the given seconds, triangles a second. One JSON line on stdout, the line on stderr. GPU engineer B, 14 October 2026.
#include "raster_probe.h"
#include <chrono>
#include <cstdlib>
#ifdef _WIN32
#include <windows.h>
#include <GL/gl.h>
#else
#include <EGL/egl.h>
#include <EGL/eglext.h>
#include <GL/gl.h>
#endif
#ifndef GL_ARRAY_BUFFER
#define GL_ARRAY_BUFFER 0x8892
#endif
#define GL_STATIC_DRAW_ 0x88E4
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
typedef void (*PF_GenVertexArrays)(GLsizei, GLuint *); typedef void (*PF_BindVertexArray)(GLuint); typedef void (*PF_EnableVertexAttribArray)(GLuint);
typedef void (*PF_VertexAttribPointer)(GLuint, GLint, GLenum, GLboolean, GLsizei, const void *); typedef void (*PF_GenFramebuffers)(GLsizei, GLuint *); typedef void (*PF_BindFramebuffer)(GLenum, GLuint);
typedef void (*PF_FramebufferTexture2D)(GLenum, GLenum, GLenum, GLuint, GLint); typedef GLenum (*PF_CheckFramebufferStatus)(GLenum); typedef void (*PF_BlendEquation)(GLenum); typedef void (*PF_BindAttribLocation)(GLuint, GLuint, const GLchar_ *);
static void *gl_proc(const char *n) {
#ifdef _WIN32
    void *p = (void *)wglGetProcAddress(n); if (!p) { static HMODULE m = LoadLibraryA("opengl32.dll"); p = (void *)GetProcAddress(m, n); } return p;
#else
    return (void *)eglGetProcAddress(n);
#endif
}
#define LOAD(T, n) T n = (T)gl_proc("gl" #n); if (!n) { printf("{\"raster_probe\": {\"backend\": \"opengl\", \"refused\": \"gl" #n " missing\"}}\n"); return 2; }
int main(int argc, char **argv) {
    double loop_s = argc > 1 ? atof(argv[1]) : 0.0;   // the digest run by default; the cost loop by its seconds
    std::string device = "?";
#ifdef _WIN32
    WNDCLASSA wc = {}; wc.lpfnWndProc = DefWindowProcA; wc.hInstance = GetModuleHandleA(nullptr); wc.lpszClassName = "igneum_raster_probe"; wc.style = CS_OWNDC; RegisterClassA(&wc);
    HWND hw = CreateWindowA(wc.lpszClassName, "igneum raster probe", WS_OVERLAPPEDWINDOW, 0, 0, 64, 64, nullptr, nullptr, wc.hInstance, nullptr);
    HDC dc = GetDC(hw); PIXELFORMATDESCRIPTOR pfd = {}; pfd.nSize = sizeof pfd; pfd.nVersion = 1; pfd.dwFlags = PFD_DRAW_TO_WINDOW | PFD_SUPPORT_OPENGL; pfd.iPixelType = PFD_TYPE_RGBA; pfd.cColorBits = 32;
    int pf = ChoosePixelFormat(dc, &pfd); if (!pf || !SetPixelFormat(dc, pf, &pfd)) { printf("{\"raster_probe\": {\"backend\": \"opengl\", \"refused\": \"no pixel format\"}}\n"); return 2; }
    HGLRC rc = wglCreateContext(dc); if (!rc || !wglMakeCurrent(dc, rc)) { printf("{\"raster_probe\": {\"backend\": \"opengl\", \"refused\": \"no WGL context\"}}\n"); return 2; }
    {   // a 3.3 core context through wglCreateContextAttribsARB when the driver offers it
        typedef HGLRC (WINAPI *PF_CCA)(HDC, HGLRC, const int *); PF_CCA cca = (PF_CCA)wglGetProcAddress("wglCreateContextAttribsARB");
        if (cca) { const int attr[] = {0x2091, 3, 0x2092, 3, 0x9126, 0x00000001, 0}; HGLRC rc2 = cca(dc, nullptr, attr); if (rc2) { wglMakeCurrent(dc, rc2); wglDeleteContext(rc); rc = rc2; } }
    }
#else
    typedef EGLDisplay (*PF_GPD)(EGLenum, void *, const EGLint *); PF_GPD gpd = (PF_GPD)eglGetProcAddress("eglGetPlatformDisplayEXT");
    EGLDisplay dpy = gpd ? gpd(0x31DD /* EGL_PLATFORM_SURFACELESS_MESA */, EGL_DEFAULT_DISPLAY, nullptr) : EGL_NO_DISPLAY;
    if (dpy == EGL_NO_DISPLAY) dpy = eglGetDisplay(EGL_DEFAULT_DISPLAY);
    EGLint maj = 0, mnr = 0; if (dpy == EGL_NO_DISPLAY || !eglInitialize(dpy, &maj, &mnr)) { printf("{\"raster_probe\": {\"backend\": \"opengl\", \"refused\": \"no EGL display (surfaceless)\"}}\n"); return 2; }
    const EGLint cfg_attr[] = { EGL_SURFACE_TYPE, EGL_PBUFFER_BIT, EGL_RENDERABLE_TYPE, EGL_OPENGL_BIT, EGL_RED_SIZE, 8, EGL_GREEN_SIZE, 8, EGL_BLUE_SIZE, 8, EGL_ALPHA_SIZE, 8, EGL_NONE };
    EGLConfig cfg; EGLint ncfg = 0; eglChooseConfig(dpy, cfg_attr, &cfg, 1, &ncfg);
    eglBindAPI(EGL_OPENGL_API);
    const EGLint ctx_attr[] = { EGL_CONTEXT_MAJOR_VERSION, 3, EGL_CONTEXT_MINOR_VERSION, 3, EGL_CONTEXT_OPENGL_PROFILE_MASK, EGL_CONTEXT_OPENGL_CORE_PROFILE_BIT, EGL_NONE };
    EGLContext ctx = eglCreateContext(dpy, ncfg ? cfg : nullptr, EGL_NO_CONTEXT, ctx_attr);
    if (ctx == EGL_NO_CONTEXT || !eglMakeCurrent(dpy, EGL_NO_SURFACE, EGL_NO_SURFACE, ctx)) { printf("{\"raster_probe\": {\"backend\": \"opengl\", \"refused\": \"no GL 3.3 core context on EGL (surfaceless)\"}}\n"); return 2; }
#endif
    { const char *r = (const char *)glGetString(GL_RENDERER), *v = (const char *)glGetString(GL_VERSION); device = std::string(r ? r : "?") + " (" + (v ? v : "?") + ")"; }
    LOAD(PF_CreateShader, CreateShader) LOAD(PF_ShaderSource, ShaderSource) LOAD(PF_CompileShader, CompileShader) LOAD(PF_GetShaderiv, GetShaderiv) LOAD(PF_GetShaderInfoLog, GetShaderInfoLog)
    LOAD(PF_CreateProgram, CreateProgram) LOAD(PF_AttachShader, AttachShader) LOAD(PF_LinkProgram, LinkProgram) LOAD(PF_GetProgramiv, GetProgramiv) LOAD(PF_UseProgram, UseProgram)
    LOAD(PF_GenBuffers, GenBuffers) LOAD(PF_BindBuffer, BindBuffer) LOAD(PF_BufferData, BufferData) LOAD(PF_GenVertexArrays, GenVertexArrays) LOAD(PF_BindVertexArray, BindVertexArray)
    LOAD(PF_EnableVertexAttribArray, EnableVertexAttribArray) LOAD(PF_VertexAttribPointer, VertexAttribPointer) LOAD(PF_GenFramebuffers, GenFramebuffers) LOAD(PF_BindFramebuffer, BindFramebuffer)
    LOAD(PF_FramebufferTexture2D, FramebufferTexture2D) LOAD(PF_CheckFramebufferStatus, CheckFramebufferStatus) LOAD(PF_BlendEquation, BlendEquation) LOAD(PF_BindAttribLocation, BindAttribLocation)
    using namespace igm;
    // the shaders
    const char *vs = "#version 330 core\nlayout(location = 0) in vec2 p; layout(location = 1) in vec4 c; out vec4 vc; void main() { gl_Position = vec4(p, 0.0, 1.0); vc = c; }\n";
    const char *fs = "#version 330 core\nin vec4 vc; out vec4 o; void main() { o = vc; }\n";
    GLuint sv = CreateShader(GL_VERTEX_SHADER_), sf = CreateShader(GL_FRAGMENT_SHADER_); ShaderSource(sv, 1, &vs, nullptr); CompileShader(sv); ShaderSource(sf, 1, &fs, nullptr); CompileShader(sf);
    GLint ok = 0; GetShaderiv(sv, GL_COMPILE_STATUS_, &ok); GLint ok2 = 0; GetShaderiv(sf, GL_COMPILE_STATUS_, &ok2);
    if (!ok || !ok2) { char log[1024] = {0}; GetShaderInfoLog(ok ? sf : sv, sizeof log - 1, nullptr, log); printf("{\"raster_probe\": {\"backend\": \"opengl\", \"refused\": \"shader: %s\"}}\n", std::string(log).substr(0, 300).c_str()); return 2; }
    GLuint pr = CreateProgram(); AttachShader(pr, sv); AttachShader(pr, sf); BindAttribLocation(pr, 0, "p"); BindAttribLocation(pr, 1, "c"); LinkProgram(pr); GetProgramiv(pr, GL_LINK_STATUS_, &ok);
    if (!ok) { printf("{\"raster_probe\": {\"backend\": \"opengl\", \"refused\": \"link\"}}\n"); return 2; }
    // the framebuffer texture
    GLuint tex = 0; glGenTextures(1, &tex); glBindTexture(GL_TEXTURE_2D, tex); glTexImage2D(GL_TEXTURE_2D, 0, GL_RGBA8, kRpSize, kRpSize, 0, GL_RGBA, GL_UNSIGNED_BYTE, nullptr);
    glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, GL_NEAREST); glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, GL_NEAREST);
    GLuint fbo = 0; GenFramebuffers(1, &fbo); BindFramebuffer(GL_FRAMEBUFFER_, fbo); FramebufferTexture2D(GL_FRAMEBUFFER_, GL_COLOR_ATTACHMENT0_, GL_TEXTURE_2D, tex, 0);
    if (CheckFramebufferStatus(GL_FRAMEBUFFER_) != GL_FRAMEBUFFER_COMPLETE_ || glGetError() != GL_NO_ERROR) { printf("{\"raster_probe\": {\"backend\": \"opengl\", \"device\": \"%s\", \"refused\": \"the 8192 x 8192 RGBA8 framebuffer is not complete\"}}\n", device.c_str()); return 2; }
    // the vertices: NDC (x / 4096 - 1, 1 - y / 4096), the colour four normalised bytes
    struct V { float x, y; uint8_t r, g, b, a; };
    std::vector<V> verts((size_t)kRpTriangles * 3);
    for (uint32_t t = 0; t < kRpTriangles; t++) { const RpTri tr = rp_triangle(t); const int xs[3] = {tr.x0, tr.x1, tr.x2}, ys[3] = {tr.y0, tr.y1, tr.y2};
        for (int k = 0; k < 3; k++) { V &v = verts[(size_t)t * 3 + k]; v.x = (float)xs[k] / 4096.0f - 1.0f; v.y = 1.0f - (float)ys[k] / 4096.0f; v.r = (uint8_t)tr.rgba; v.g = (uint8_t)(tr.rgba >> 8); v.b = (uint8_t)(tr.rgba >> 16); v.a = (uint8_t)(tr.rgba >> 24); } }
    GLuint vao = 0, vbo = 0; GenVertexArrays(1, &vao); BindVertexArray(vao); GenBuffers(1, &vbo); BindBuffer(GL_ARRAY_BUFFER, vbo); BufferData(GL_ARRAY_BUFFER, (GLsizeiptr_)(verts.size() * sizeof(V)), verts.data(), GL_STATIC_DRAW_);
    EnableVertexAttribArray(0); VertexAttribPointer(0, 2, GL_FLOAT, GL_FALSE, sizeof(V), (const void *)0); EnableVertexAttribArray(1); VertexAttribPointer(1, 4, GL_UNSIGNED_BYTE, GL_TRUE, sizeof(V), (const void *)8);
    // the state: no depth, no multisampling, no cull, no scissor, the blend ONE + ONE ADD
    glViewport(0, 0, kRpSize, kRpSize); glDisable(GL_DEPTH_TEST); glDisable(GL_CULL_FACE); glDisable(GL_SCISSOR_TEST); glDisable(GL_MULTISAMPLE_); glDisable(GL_DITHER);
    glEnable(GL_BLEND); glBlendFunc(GL_ONE, GL_ONE); BlendEquation(GL_FUNC_ADD_);
    glClearColor(0, 0, 0, 0); glClear(GL_COLOR_BUFFER_BIT); UseProgram(pr); BindVertexArray(vao);
    glDrawArrays(GL_TRIANGLES, 0, (GLsizei)verts.size()); glFinish();
    if (glGetError() != GL_NO_ERROR) { printf("{\"raster_probe\": {\"backend\": \"opengl\", \"device\": \"%s\", \"refused\": \"a GL error after the draw\"}}\n", device.c_str()); return 2; }
    // the readback, rows reversed to the probe's top-left order
    std::vector<uint8_t> raw((size_t)kRpSize * kRpSize * 4), card(raw.size());
    glPixelStorei(GL_PACK_ALIGNMENT, 1); glReadPixels(0, 0, kRpSize, kRpSize, GL_RGBA, GL_UNSIGNED_BYTE, raw.data());
    for (uint32_t j = 0; j < kRpSize; j++) std::memcpy(&card[(size_t)j * kRpSize * 4], &raw[(size_t)(kRpSize - 1 - j) * kRpSize * 4], (size_t)kRpSize * 4);
    // the cost loop: the same draw repeated
    double tri_s = 0, secs = 0;
    if (loop_s > 0) { auto t0 = std::chrono::steady_clock::now(); uint64_t draws = 0; while (std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count() < loop_s) { glDrawArrays(GL_TRIANGLES, 0, (GLsizei)verts.size()); draws++; if ((draws & 7) == 0) glFinish(); } glFinish(); secs = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count(); tri_s = (double)draws * kRpTriangles / secs; }
    return rp_report("opengl", device, card, "GL 3.3 core, one draw of 3,000,000 vertices, blend ONE ONE ADD, FBO texture RGBA8, glReadPixels rows reversed", tri_s, secs, "the plug or the board meter beside the job (not read by the probe)");
}
