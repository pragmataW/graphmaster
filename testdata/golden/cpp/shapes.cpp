#include "shapes.hpp"

#include <algorithm>
#include <iostream>

using namespace geo;
using std::cout;
namespace fs = std::filesystem;

int Shape::count = 0;

Shape::~Shape() = default;

std::string Shape::label() const { return "shape"; }

Rect::Rect(double w, double h) : w_(w), h_(h) { ++count; }

double Rect::area() const {
    return util::clamp(w_, 0, 1e9) * geo::util::clamp(h_, 0, 1e9);
}

bool Rect::operator==(const Rect& other) const {
    return this->area() == other.area() && width() == other.width();
}

template <typename T>
void Box<T>::set(const T& v) {
    value_ = v;
}

namespace geo {
Square::Square(double side) : Rect(side, side) {}

std::string Square::label() const { return "square:" + Shape::label(); }

namespace util {
double clamp(double v, double lo, double hi) { return std::min(std::max(v, lo), hi); }
}  // namespace util
}  // namespace geo

int geo::detail::impl::helper(int x) { return x * 2; }

static void report(const Shape& s) {
    cout << s.label() << " " << s.area() << "\n";
}

int main() {
    auto r = std::make_unique<geo::Rect>(2.0, 3.0);
    Shape* sq = new Square(4.0);
    ShapeList list;
    list.push_back(std::move(r));
    report(*sq);
    Box<int> box;
    box.set(geo::util::clamp(1.0, 0.0, 2.0) > 0 ? 1 : 0);
    delete sq;
    return box.get();
}
