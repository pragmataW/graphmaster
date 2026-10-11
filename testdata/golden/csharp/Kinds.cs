global using global::System.Text;
using static System.Math;
using Json = Newtonsoft.Json.JsonConvert;
using Fixture.Billing;

namespace Outer
{
    namespace Inner.Deep
    {
        public delegate void Handler(object sender);

        public enum Color : byte
        {
            Red = 1,
            Green,
        }

        internal interface IShape : IComparable<IShape>, Fixture.Contracts.IDrawable
        {
            double Area();
            string Name { get; }
        }

        public partial record struct Point(int X, int Y) : IShape;

        public record Labeled<T>(string Label, T Value) : Base<T>(Value), IShape where T : class;

        public sealed class Shape<T> : Fixture.Billing.PaymentBase, IShape
        {
            public const int Sides = 4;
            private static readonly List<T> cache = new(), spare = null;
            public event Handler Changed;
            public event Handler Moved { add { } remove { } }

            public Shape(int sides) : base()
            {
                this.Reset<T>();
                Changed?.Invoke(this, null);
            }

            public double Area() => Abs(Sides * 2.0);

            public string Name { get; } = nameof(Shape<T>);

            protected internal T Pick(Fixture.Billing.Invoice invoice, int index)
            {
                Outer.Inner.Deep.Color c = (Color)index;
                var json = Json.SerializeObject(invoice as object);
                if (invoice is Invoice) { base.WithFee(1m); }
                return Local(index);

                T Local(int i) => cache[i];
            }

            private void Reset<U>() where U : T
            {
                typeof(U).ToString();
                foreach (Point p in new List<Point>()) { p.Area(); }
            }

            public class Nested
            {
                internal Nested() { }
            }
        }
    }
}
