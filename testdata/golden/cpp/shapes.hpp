#pragma once

#include <memory>
#include <string>
#include <vector>

#include "geo/base.hpp"

namespace geo {

class Shape {
public:
    Shape() = default;
    virtual ~Shape();
    virtual double area() const = 0;
    virtual std::string label() const;
    static int count;

protected:
    int id_ = 0;
};

class Rect : public Shape {
public:
    Rect(double w, double h);
    double area() const override;
    double width() const { return w_; }
    bool operator==(const Rect& other) const;

private:
    double w_;
    double h_;
    friend class Canvas;
    friend bool same(const Rect& a, const Rect& b);
};

struct Square final : public Rect, private detail::Tagged<Square> {
    explicit Square(double side);
    std::string label() const override;
};

template <typename T>
class Box {
public:
    T get() const { return value_; }
    void set(const T& v);

private:
    T value_;
};

enum class Color { Red, Green = 3 };

using ShapePtr = std::unique_ptr<Shape>;
typedef std::vector<ShapePtr> ShapeList;

namespace util {
double clamp(double v, double lo, double hi);
constexpr double kEpsilon = 1e-9;
}  // namespace util

namespace detail::impl {
int helper(int x);
}

}  // namespace geo

namespace {
int hidden_counter = 0;
}
