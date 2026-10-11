use fixture_shapes::geometry::total_area;
use fixture_shapes::shapes::{circle::Circle, square::Square};
use fixture_shapes::Shape;

fn main() {
    let shapes: Vec<Box<dyn Shape>> = vec![Box::new(Circle::new(1.0)), Box::new(Square::new(2.0))];
    println!("total area: {:.2}", total_area(&shapes));
}
