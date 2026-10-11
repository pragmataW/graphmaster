#include <iostream>
#include <memory>

#include "geo/canvas.hpp"

int main() {
    geo::Canvas canvas;
    canvas.add(std::make_unique<geo::Rect>(2.0, 3.0));
    canvas.add(std::make_unique<geo::Square>(4.0));
    std::cout << canvas.totalArea() << " over " << canvas.size() << " shapes\n";
    return 0;
}
