// The Glow clock style's colors moving on from where the cores drew them (glow.go, glow_gpu.go): a
// pass, at the size of the cores' field, a pixel to each of its points. Out: how much the field has
// changed at the point since the frame began, `since` seconds ago, a quarter of it about a half.
//
//   p[0]  the field's size in points
//   p[1] to p[3]  the three colors, p[4] the ground (0 to 1)
//   p[5] to p[8]  each blob's path: where on it in x and y at the frame's start (radians), and how fast
//                 (radians a second)
//   p[9] to p[12] each blob's size squared (in points), and which color it is

vec3 blob(vec2 q, float t, vec4 path, vec4 b, inout float total) {
	vec2 at = vec2(0.5 + 0.38 * sin(path.x + path.z * t), 0.5 + 0.36 * sin(path.y + path.w * t)) * p[0].xy;
	vec2 d = q - at;
	float v = b.x / (dot(d, d) + b.x * 0.25);
	total += v;
	return v * (b.y < 0.5 ? vec3(1.0, 0.0, 0.0) : b.y < 1.5 ? vec3(0.0, 1.0, 0.0) : vec3(0.0, 0.0, 1.0));
}

vec3 field(vec2 q, float t) {
	float total = 0.0;
	vec3 w = blob(q, t, p[5], p[9], total) + blob(q, t, p[6], p[10], total) + blob(q, t, p[7], p[11], total) +
		 blob(q, t, p[8], p[12], total);
	float k = 0.5 * smoothstep(0.35, 1.6, total);
	vec3 c = (w.x * p[1].rgb + w.y * p[2].rgb + w.z * p[3].rgb) / total;
	return p[4].rgb * (1.0 - k) + c * k;
}

vec4 pass(vec2 h) {
	vec2 q = h - 0.5; // the point, as glowField counts them
	return vec4(0.5 + (field(q, since) - field(q, 0.0)) * 4.0, 1.0);
}
