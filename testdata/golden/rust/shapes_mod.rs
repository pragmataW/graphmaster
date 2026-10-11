pub mod circle;
pub mod square;

/// A closed 2D shape.
pub trait Shape {
    fn area(&self) -> f64;

    fn name(&self) -> String {
        String::from("shape")
    }
}
