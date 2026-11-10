// Rain on the glass over the weather page (weather_gpu.go, weatherfx_glass.go): drops that land, sit and
// dry, or run down in fits leaving a wet trail with beads; a mist of tiny ones. Each a lens over the page.
//
//   p[0]  the strength, and the page's scale for 1, 5 and 40 of the Show 5's pixels
//   p[1]  the scale for 14 of them
//
// Each item is a drop's life (glassRain's cycle), from the frame's start:
//
//   a drop (0) or its trail (3)  a0: where it lands, its radius, when its life starts
//                                a1: how long it lives, when it starts running (below 0: never), how
//                                    fast, how often it sticks
//                                a2: how far it runs at most, the two phases of its wander, the kind
//   mist (1)                     a0 as a drop's, a1.x its life; a2.w the kind
//   a bead (2)                   a0: where it is, its radius, when the drop's life starts; a1 the
//                                drop's; a2: how far the drop runs at most, how far down the bead is,
//                                the drop's radius, the kind
//
// a2.w is the kind plus a number of the drop's own, under 1.

varying vec2 c;
varying vec4 drop; // middle, radius, how stretched it runs
varying vec4 look; // strength, the kind, its number, the time it has run
varying vec4 path; // where it landed, how fast it runs, how far it has run
varying vec2 ph;   // its wander's phases

const vec3 light = vec3(0.957, 0.973, 1.0);
const vec3 shade = vec3(0.063, 0.086, 0.125);

float wander(float d, vec2 at) {
	return p[0].z * (0.65 * sin(d / p[0].w + at.x) + 0.35 * sin(d / p[1].x + at.y) - 0.65 * sin(at.x) - 0.35 * sin(at.y));
}

#ifdef VERTEX
void item() {
	float kind = floor(a2.w);
	float u = since - a0.w;
	float period = a1.x;
	vec2 at0 = a0.xy;
	float r = a0.z, stretch = 0.0, fade = 0.0, run = -1.0, dist = 0.0;
	if (u >= 0.0 && u <= period) {
		if (kind == 1.0) {
			fade = clamp(u / 0.3, 0.0, 1.0) * clamp((period - u) / 3.0, 0.0, 1.0) * p[0].x * 0.8;
		} else {
			float landed = clamp(u / 0.15, 0.0, 1.0);
			fade = landed * clamp((period - u) / 1.2, 0.0, 1.0) * p[0].x;
			if (a1.y >= 0.0 && u >= a1.y) {
				run = u - a1.y;
				float w = 6.2831853 * a1.w;
				dist = min(a1.z * (run - sin(w * run) / w), a2.x);
			}
			if (kind == 0.0 && run < 0.0) {
				r *= 0.7 + 0.3 * landed;
			} else if (kind == 0.0) {
				at0 += vec2(wander(dist, a2.yz), dist);
				r *= 1.0 - 0.3 * clamp(run / 2.0, 0.0, 1.0);
				stretch = 0.5 * clamp(run / 0.4, 0.0, 1.0);
			} else if (kind == 2.0) {
				// Left behind once the drop is two of its radii past it, drying more slowly than the trail.
				fade = run >= 0.0 && a2.y < dist - 2.0 * a2.z ? fade * exp(-max(0.0, run - a2.y / a1.z) / 5.0) : 0.0;
			}
		}
	}
	vec2 lo, hi;
	if (kind == 3.0) {
		float m = 2.0 * p[0].z + 2.0 * max(1.0, r * 0.26) + 2.0;
		lo = vec2(a0.x - m, a0.y - 1.0);
		hi = vec2(a0.x + m, a0.y + dist + 1.0);
		if (run < 0.0 || dist <= 0.0)
			fade = 0.0;
	} else {
		float reach = r * 1.25 + 2.0;
		lo = at0 - vec2(reach, reach * (1.0 + 0.25 * stretch));
		hi = at0 + vec2(reach, reach * (1.0 + stretch));
	}
	if (fade <= 0.0 || (kind != 3.0 && r < 0.8)) {
		lo = vec2(-10.0);
		hi = vec2(-10.0);
	}
	c = mix(lo, hi, corner);
	drop = vec4(at0, r, stretch);
	look = vec4(fade, kind, fract(a2.w), run);
	path = vec4(a0.xy, a1.z, dist);
	ph = a2.yz;
	gl_Position = at(c);
}
#else
float hash(float s, float k) {
	return fract(sin(s * 12.9898 + k * 78.233) * 43758.5453);
}

vec4 disc(vec2 q, float r, vec3 col, float a) {
	float k = clamp(0.5 - (length(q) - r), 0.0, 1.0) * a;
	return vec4(col * k, k);
}

vec4 over(vec4 top, vec4 under) {
	return top + under * (1.0 - top.a);
}

vec4 trail() {
	float d = c.y - 0.5 - path.y;
	if (d < 0.0 || d > path.w)
		return vec4(0.0);
	float age = max(0.0, look.w - d / path.z);
	float a = look.x * exp(-age / 1.8);
	if (a < 0.03)
		return vec4(0.0);
	float wet = max(1.0, drop.z * 0.26);
	float thin = 1.0 - 0.4 * clamp(age / 4.0, 0.0, 1.0);
	float x = c.x - (path.x + wander(d, ph));
	float s = clamp(0.5 - (abs(x) - wet * thin), 0.0, 1.0) * 0.1 * a;
	float l = clamp(0.5 - (abs(x + wet * 0.4) - wet * 0.3 * thin), 0.0, 1.0) * 0.16 * a;
	return over(vec4(light * l, l), vec4(shade * s, s));
}

vec4 color() {
	if (look.y == 3.0)
		return trail();
	vec2 ctr = drop.xy;
	float r = drop.z, alpha = look.x;
	if (r < 2.5) {
		// Too small to show a lens: a speck of shade with the light caught on its upper side.
		vec4 s = disc(c - ctr, r, shade, 0.28 * alpha);
		return over(disc(c - ctr + vec2(0.3 * r, 0.35 * r), max(0.5, 0.45 * r), light, 0.6 * alpha), s);
	}
	float stretch = drop.w;
	vec2 dd = c - ctr;
	float ny = dd.y * (dd.y > 0.0 ? 1.0 / (1.0 + stretch) : 1.0 / (1.0 + 0.25 * stretch));
	float nx = dd.x * (1.0 + 0.2 * stretch);
	float d = sqrt(nx * nx + ny * ny);
	float sn = d > 0.0 ? ny / d : 0.0, cs = d > 0.0 ? nx / d : 1.0;
	float k2 = hash(look.z, 11.0) * 6.3, k3 = hash(look.z, 12.0) * 6.3;
	float a2 = 0.05 + 0.07 * hash(look.z, 14.0), a3 = 0.03 + 0.05 * hash(look.z, 15.0);
	float sin2 = 2.0 * sn * cs, cos2 = cs * cs - sn * sn;
	float sin3 = sn * (3.0 - 4.0 * sn * sn), cos3 = cs * (4.0 * cs * cs - 3.0);
	float edge = r * (1.0 + a2 * (sin2 * cos(k2) + cos2 * sin(k2)) + a3 * (sin3 * cos(k3) + cos3 * sin(k3)));
	float cover = clamp(edge - d + 0.5, 0.0, 1.0) * alpha;
	if (cover <= 0.0)
		return vec4(0.0);
	float q = clamp(d / edge, 0.0, 1.0);
	// The lens: the page behind, turned over and made smaller toward the middle.
	float k = 0.55 * (1.0 - 0.35 * q * q);
	vec3 col = page(ctr - dd * k) * 0.9 + light * 0.1;
	float top = 0.5 - 0.5 * sn;
	col = mix(col, shade, smoothstep(0.6, 1.0, q) * (0.22 + 0.45 * top));
	vec2 h = (c - (ctr - vec2(0.3 * r, 0.36 * r))) / (0.15 * r + 0.5);
	float h2 = dot(h, h);
	float lit = h2 < 4.0 ? 0.85 * exp(-2.0 * h2) : 0.0;
	if (q > 0.4 && top < 0.5)
		lit += 0.4 * smoothstep(0.4, 0.82, q) * (1.0 - smoothstep(0.82, 1.0, q)) * (1.0 - top);
	col = mix(col, light, clamp(lit, 0.0, 1.0));
	cover = min(cover, 1.0);
	return vec4(col * cover, cover);
}
#endif
