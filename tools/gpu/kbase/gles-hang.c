// SPDX-License-Identifier: MIT
//
// gles-hang: a job the GPU cannot finish, to see what the stack makes of one. A fragment shader with
// loops long enough that a tile takes seconds is drawn over a target; the kernel driver stops a job
// that runs too long (on kbase: a soft stop after 100 ms, which such a job ignores, a hard stop after
// 5 s), and what matters is that the caller gets control back, that GL goes on working after, and
// that the next process finds the GPU as it was. After the stuck frame a plain one is drawn and read
// back, to show that.
//
//   gles-hang [-s size] [-l loops]

#include <EGL/egl.h>
#include <EGL/eglext.h>
#include <GLES2/gl2.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

static double now_ms(void)
{
	struct timespec t;
	clock_gettime(CLOCK_MONOTONIC, &t);
	return t.tv_sec * 1e3 + t.tv_nsec / 1e6;
}

#define die(...) do { fprintf(stderr, "gles-hang: " __VA_ARGS__); fprintf(stderr, " (EGL 0x%x, GL 0x%x)\n", eglGetError(), glGetError()); exit(1); } while (0)

static GLuint program(const char *fs)
{
	static const char *vs = "attribute vec2 pos; void main() { gl_Position = vec4(pos, 0.0, 1.0); }\n";
	GLuint p = glCreateProgram();
	const char *src[2] = {vs, fs};
	for (int i = 0; i < 2; i++) {
		GLuint s = glCreateShader(i ? GL_FRAGMENT_SHADER : GL_VERTEX_SHADER);
		GLint ok = 0;
		glShaderSource(s, 1, &src[i], NULL);
		glCompileShader(s);
		glGetShaderiv(s, GL_COMPILE_STATUS, &ok);
		if (!ok) {
			char log[1024];
			glGetShaderInfoLog(s, sizeof log, NULL, log);
			die("shader: %s", log);
		}
		glAttachShader(p, s);
	}
	glBindAttribLocation(p, 0, "pos");
	glLinkProgram(p);
	GLint ok = 0;
	glGetProgramiv(p, GL_LINK_STATUS, &ok);
	if (!ok)
		die("link");
	return p;
}

int main(int argc, char **argv)
{
	int size = 256, loops = 4000;
	for (int i = 1; i + 1 < argc; i += 2) {
		if (!strcmp(argv[i], "-s"))
			size = atoi(argv[i + 1]);
		else if (!strcmp(argv[i], "-l"))
			loops = atoi(argv[i + 1]);
	}
	EGLDisplay dpy = eglGetPlatformDisplay(EGL_PLATFORM_SURFACELESS_MESA, EGL_DEFAULT_DISPLAY, NULL);
	if (dpy == EGL_NO_DISPLAY || !eglInitialize(dpy, NULL, NULL))
		die("no EGL display");
	eglBindAPI(EGL_OPENGL_ES_API);
	EGLConfig cfg;
	EGLint n = 0;
	static const EGLint cfg_attrs[] = {EGL_SURFACE_TYPE, EGL_PBUFFER_BIT, EGL_RENDERABLE_TYPE, EGL_OPENGL_ES2_BIT, EGL_NONE};
	static const EGLint ctx_attrs[] = {EGL_CONTEXT_CLIENT_VERSION, 2, EGL_NONE};
	if (!eglChooseConfig(dpy, cfg_attrs, &cfg, 1, &n) || n < 1)
		die("no EGL config");
	EGLContext ctx = eglCreateContext(dpy, cfg, EGL_NO_CONTEXT, ctx_attrs);
	if (ctx == EGL_NO_CONTEXT || !eglMakeCurrent(dpy, EGL_NO_SURFACE, EGL_NO_SURFACE, ctx))
		die("no GLES 2 context");
	printf("renderer %s\n", glGetString(GL_RENDERER));

	GLuint tex, fbo;
	glGenTextures(1, &tex);
	glBindTexture(GL_TEXTURE_2D, tex);
	glTexImage2D(GL_TEXTURE_2D, 0, GL_RGBA, size, size, 0, GL_RGBA, GL_UNSIGNED_BYTE, NULL);
	glGenFramebuffers(1, &fbo);
	glBindFramebuffer(GL_FRAMEBUFFER, fbo);
	glFramebufferTexture2D(GL_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_TEXTURE_2D, tex, 0);
	glViewport(0, 0, size, size);
	static const GLfloat quad[] = {-1, -1, 1, -1, -1, 1, 1, 1};
	glVertexAttribPointer(0, 2, GL_FLOAT, GL_FALSE, 0, quad);
	glEnableVertexAttribArray(0);

	// The loops' result depends on a uniform, so the compiler cannot work it out beforehand.
	char fs[1024];
	snprintf(fs, sizeof fs,
		 "precision highp float; uniform float k;\n"
		 "void main() {\n"
		 "  float a = gl_FragCoord.x * k;\n"
		 "  for (int i = 0; i < %d; i++)\n"
		 "    for (int j = 0; j < 1000; j++)\n"
		 "      a = fract(sin(a + float(j)) * 43758.5453 + k);\n"
		 "  gl_FragColor = vec4(a, 0.5, 0.25, 1.0);\n"
		 "}\n", loops);
	GLuint slow = program(fs);
	GLuint plain = program("precision mediump float; void main() { gl_FragColor = vec4(0.2, 0.4, 0.6, 1.0); }\n");

	unsigned char px[4] = {0};
	glUseProgram(slow);
	glUniform1f(glGetUniformLocation(slow, "k"), 0.731f);
	double t = now_ms();
	glDrawArrays(GL_TRIANGLE_STRIP, 0, 4);
	glFinish();
	printf("the job that cannot finish: control back after %.0f ms, GL error 0x%x\n", now_ms() - t, glGetError());

	glUseProgram(plain);
	t = now_ms();
	glDrawArrays(GL_TRIANGLE_STRIP, 0, 4);
	glReadPixels(size / 2, size / 2, 1, 1, GL_RGBA, GL_UNSIGNED_BYTE, px);
	int ok = px[0] == 51 && px[1] == 102 && px[2] == 153;
	printf("a plain frame after it: %.1f ms, pixel %u %u %u (want 51 102 153): %s\n", now_ms() - t, px[0], px[1], px[2],
	       ok ? "ok" : "WRONG");
	return !ok;
}
