#pragma once

#include <memory>
#include <vector>

#include "geo/shape.hpp"

namespace geo {

class Canvas {
public:
    void add(std::unique_ptr<Shape> shape);
    double totalArea() const;
    std::size_t size() const { return shapes_.size(); }

private:
    std::vector<std::unique_ptr<Shape>> shapes_;
};

}  // namespace geo
