use fixture_shapes::geometry::total_area;
use fixture_shapes::shapes::square::Square;
use fixture_shapes::Shape;

#[test]
fn squares_add_up() {
    let shapes: Vec<Box<dyn Shape>> = vec![Box::new(Square::new(1.0)), Box::new(Square::new(2.0))];
    assert_eq!(total_area(&shapes), 5.0);
}
