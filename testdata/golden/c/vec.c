#include <math.h>

#include "vec.h"

static double sq(double v) {
    return v * v;
}

Vec2 vec_add(Vec2 a, Vec2 b) {
    Vec2 r = {a.x + b.x, a.y + b.y};
    return r;
}

Vec2 vec_scale(Vec2 v, double k) {
    Vec2 r = {v.x * k, v.y * k};
    return r;
}

double vec_len(Vec2 v) {
    return sqrt(sq(v.x) + sq(v.y));
}
