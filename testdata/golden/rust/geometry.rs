use crate::shapes::Shape;

pub fn square_of(x: f64) -> f64 {
    x * x
}

pub fn total_area(shapes: &[Box<dyn Shape>]) -> f64 {
    shapes.iter().map(|s| s.area()).sum()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn square_of_negative() {
        assert_eq!(square_of(-3.0), 9.0);
    }
}
