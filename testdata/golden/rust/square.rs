use crate::geometry;
use crate::shapes::Shape;

#[derive(Debug, Clone, Copy)]
pub struct Square {
    pub side: f64,
}

impl Square {
    pub fn new(side: f64) -> Self {
        Square { side }
    }
}

impl Shape for Square {
    fn area(&self) -> f64 {
        geometry::square_of(self.side)
    }
}
