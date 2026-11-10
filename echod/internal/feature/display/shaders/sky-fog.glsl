// Fog over the weather page (weather_gpu.go, weatherfx.go's fog): soft bands drifting across.
//
//   p[0]  the color
//   a0    the band's middle at rest, the drift's phase at the frame's start and its reach, half its height
//   a1    the strength's phase and the ripple's at the frame's start, the page's left and right

varying vec2 c;
varying vec4 band; // middle, half, strength, the ripple's phase

#ifdef VERTEX
void item() {
	float mid = a0.x + sin(a0.y + since * 0.15) * a0.z;
	c = mix(vec2(a1.z, mid - a0.w), vec2(a1.w, mid + a0.w), corner);
	band = vec4(mid, a0.w, 0.10 + 0.06 * sin(a1.x + since * 0.25), a1.y + since * 0.4);
	gl_Position = at(c);
}
#else
vec4 color() {
	float d = abs(c.y - 0.5 - band.x) / band.y;
	float row = band.z * (1.0 - d * d);
	float a = row > 0.005 ? row * (0.75 + 0.25 * sin((c.x - 0.5) * 0.012 + band.w)) : 0.0;
	return vec4(p[0].rgb * a, a);
}
#endif
