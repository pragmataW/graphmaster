#ifndef FIXTURE_VEC_H
#define FIXTURE_VEC_H

/* A 2D vector. */
typedef struct {
    double x;
    double y;
} Vec2;

#define VEC2_ZERO ((Vec2){0.0, 0.0})

Vec2 vec_add(Vec2 a, Vec2 b);
Vec2 vec_scale(Vec2 v, double k);
double vec_len(Vec2 v);

#endif /* FIXTURE_VEC_H */
