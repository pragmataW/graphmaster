//! Covers the Rust constructs graphmaster extracts.

use std::collections::{HashMap, hash_map::Entry as MapEntry};
use std::fmt::{self, Display};
use crate::shapes::Shape;
use super::helpers::*;
use self::inner::Helper as InnerHelper;

pub mod geometry;
pub(crate) mod util;

pub const LIMIT: usize = 8;
static mut COUNTER: u32 = 0;

pub type Registry<T> = HashMap<String, Vec<T>>;

/// A point.
#[derive(Debug, Clone)]
pub struct Point<T> {
    pub x: T,
    y: T,
}

pub struct Meters(pub f64);

pub union Bits {
    int: u32,
    float: f32,
}

pub enum Event {
    Click { x: i32, y: i32 },
    Key(char),
    Quit,
}

pub trait Named: Display + fmt::Debug {
    const KIND: &'static str;
    type Id;

    fn name(&self) -> String;

    fn describe(&self) -> String {
        format!("{} {}", Self::KIND, self.name())
    }
}

impl<T: Copy + Into<f64>> Point<T> {
    pub const ORIGIN_NAME: &'static str = "origin";

    pub fn new(x: T, y: T) -> Self {
        Point { x, y }
    }

    pub fn norm(&self) -> f64 {
        let dx: f64 = self.x.into();
        let dy: f64 = self.y.into();
        geometry::hypot(dx, dy)
    }

    fn twice(&self) -> Self {
        Self::new(self.x, self.y)
    }
}

impl<T> Display for Point<T> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "point")
    }
}

impl<T: fmt::Debug> Named for Point<T> {
    const KIND: &'static str = "point";
    type Id = u32;

    fn name(&self) -> String {
        String::from("p")
    }
}

impl crate::shapes::Shape for Meters {
    fn area(&self) -> f64 {
        crate::geometry::square_of(self.0)
    }
}

impl Remote for Meters {}

impl Clone for Remote {
    fn clone(&self) -> Self {
        Remote::make()
    }
}

macro_rules! square {
    ($x:expr) => {
        $x * $x
    };
}

pub fn handle(event: Event, reg: &mut Registry<Point<i32>>) -> Option<usize> {
    fn local_helper(n: usize) -> usize {
        n + 1
    }
    let p = Point::new(1, 2);
    let total = square!(local_helper(p.norm() as usize));
    match event {
        Event::Click { x, .. } => Some(x as usize),
        Event::Key(_) => super::helpers::log("key"),
        Event::Quit => None,
    };
    let _cfg = util::Config::default();
    reg.entry("k".to_string()).or_default();
    std::mem::drop(total);
    println!("{}", util::render(&p));
    Some(LIMIT)
}

extern "C" {
    fn abs(input: i32) -> i32;
}

mod inner {
    use super::Point;

    pub struct Helper;

    impl Helper {
        pub fn run() -> Point<i32> {
            super::handle_inner();
            Point::new(0, 0)
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_point_new() {
        let p = Point::new(3, 4);
        assert_eq!(p.norm(), 5.0);
    }

    #[tokio::test]
    #[ignore]
    async fn handles_quit() {
        assert!(handle(Event::Quit, &mut HashMap::new()).is_some());
    }

    fn fixture() -> Point<i32> {
        Point::new(0, 0)
    }
}
