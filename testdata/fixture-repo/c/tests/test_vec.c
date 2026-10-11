#include <assert.h>

#include "../include/vec.h"

static void test_vec_add(void) {
    Vec2 r = vec_add((Vec2){1, 2}, (Vec2){3, 4});
    assert(r.x == 4 && r.y == 6);
}

static void test_vec_len(void) {
    assert(vec_len((Vec2){3, 4}) == 5.0);
}

int main(void) {
    test_vec_add();
    test_vec_len();
    return 0;
}
