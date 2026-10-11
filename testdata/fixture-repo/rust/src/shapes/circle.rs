use super::Shape;
use crate::geometry::square_of;

pub struct Circle {
    pub radius: f64,
}

impl Circle {
    pub fn new(radius: f64) -> Self {
        Circle { radius }
    }
}

impl Shape for Circle {
    fn area(&self) -> f64 {
        std::f64::consts::PI * square_of(self.radius)
    }

    fn name(&self) -> String {
        String::from("circle")
    }
}
