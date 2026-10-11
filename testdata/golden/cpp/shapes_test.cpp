#include <gtest/gtest.h>

#include "shapes.hpp"

class RectTest : public ::testing::Test {
protected:
    geo::Rect rect_{2.0, 3.0};
};

TEST(SquareTest, LabelIsSquare) {
    geo::Square sq(1.0);
    EXPECT_EQ(sq.label(), "square:shape");
}

TEST_F(RectTest, Area) {
    EXPECT_DOUBLE_EQ(rect_.area(), 6.0);
}

void test_clamp() {
    ASSERT_TRUE(geo::util::clamp(5, 0, 1) == 1);
}
