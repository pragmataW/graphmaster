#include "geo/shape.hpp"

#include <algorithm>

namespace geo {

Shape::~Shape() = default;

std::string Shape::label() const { return "shape"; }

Rect::Rect(double w, double h) : w_(w), h_(h) {}

double Rect::area() const {
    return util::clamp(w_, 0, 1e9) * util::clamp(h_, 0, 1e9);
}

Square::Square(double side) : Rect(side, side) {}

std::string Square::label() const { return "square"; }

namespace util {
double clamp(double v, double lo, double hi) { return std::min(std::max(v, lo), hi); }
}  // namespace util

}  // namespace geo
