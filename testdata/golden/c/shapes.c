#include <math.h>
#include <stdlib.h>

#include "shapes.h"

int shape_count = 0;
const char *shape_names[] = {"circle", "square"};
static const double PI_APPROX = 3.14159;

static double circle_area(const struct Shape *self) {
    return PI_APPROX * SQUARE(self->dim.radius);
}

Point point_make(double x, double y) {
    Point p = {x, y};
    return p;
}

double shape_area(const struct Shape *s) {
    if (!shape_valid(s)) {
        return 0.0;
    }
    return s->area(s);
}

struct Shape *shape_new(enum Kind kind) {
    struct Shape *s = calloc(1, sizeof(struct Shape));
    s->id = (shape_id)++shape_count;
    s->origin = point_make(0, 0);
    if (kind == KIND_CIRCLE) {
        s->area = circle_area;
        s->dim.radius = 1.0;
    }
    return s;
}

int **shape_grid(size_t n) {
    int **g = malloc(n * sizeof(int *));
    for (size_t i = 0; i < n && i < SHAPES_MAX; i++) {
        g[i] = NULL;
    }
    return g;
}

int main(int argc, char **argv) {
    struct Shape *s = shape_new(KIND_CIRCLE);
    Node_t head = {0};
    head.value = argc;
    printf("%f %d\n", shape_area(s), head.value);
    (free)(s);
    return 0;
}
