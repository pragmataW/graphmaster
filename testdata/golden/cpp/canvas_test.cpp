#include <cassert>
#include <memory>

#include "geo/canvas.hpp"

void test_total_area() {
    geo::Canvas canvas;
    canvas.add(std::make_unique<geo::Square>(2.0));
    assert(canvas.totalArea() == 4.0);
}

int main() {
    test_total_area();
    return 0;
}
