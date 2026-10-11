#include "unity.h"
#include "shapes.h"

void setUp(void) {}

void test_point_make(void) {
    Point p = point_make(1, 2);
    TEST_ASSERT_EQUAL(1, (int)p.x);
}

static void test_shape_area(void **state) {
    (void)state;
    struct Shape *s = shape_new(KIND_SQUARE);
    assert_true(shape_area(s) >= 0);
}

int test_helper(int x) { return x; }

int main(void) {
    UNITY_BEGIN();
    RUN_TEST(test_point_make);
    return UNITY_END();
}
