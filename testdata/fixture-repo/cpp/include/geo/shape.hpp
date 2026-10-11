#pragma once

#include <string>

namespace geo {

class Shape {
public:
    virtual ~Shape();
    virtual double area() const = 0;
    virtual std::string label() const;
};

class Rect : public Shape {
public:
    Rect(double w, double h);
    double area() const override;

private:
    double w_;
    double h_;
};

class Square final : public Rect {
public:
    explicit Square(double side);
    std::string label() const override;
};

namespace util {
double clamp(double v, double lo, double hi);
}  // namespace util

}  // namespace geo
