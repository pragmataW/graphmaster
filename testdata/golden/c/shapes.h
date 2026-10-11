#ifndef SHAPES_H
#define SHAPES_H

#include <stddef.h>
#include "config.h"

#define SHAPES_MAX 16
#define SQUARE(x) ((x) * (x))
#define SHAPES_DEBUG

typedef struct {
    double x;
    double y;
} Point;

typedef struct Node {
    struct Node *next;
    int value;
} Node_t;

typedef unsigned long shape_id;
typedef int (*shape_cb)(const Point *p, void *ctx);

struct Shape {
    shape_id id;
    Point origin;
    double (*area)(const struct Shape *self);
    char name[32];
    union {
        double radius;
        double side;
    } dim;
};

union Number {
    int i;
    float f;
};

enum Kind { KIND_CIRCLE, KIND_SQUARE = 2 };

typedef enum { COLOR_RED, COLOR_BLUE } Color;

extern int shape_count;
extern const char *shape_names[];

Point point_make(double x, double y);
double shape_area(const struct Shape *s);
static inline int shape_valid(const struct Shape *s) { return s != NULL; }
struct Shape *shape_new(enum Kind kind);
int **shape_grid(size_t n);

#endif /* SHAPES_H */
