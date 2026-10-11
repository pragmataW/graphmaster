#include "geo/canvas.hpp"

using namespace geo;

void Canvas::add(std::unique_ptr<Shape> shape) {
    shapes_.push_back(std::move(shape));
}

double Canvas::totalArea() const {
    double sum = 0;
    for (const auto& s : shapes_) {
        sum += s->area();
    }
    return sum;
}
