namespace Fixture.Contracts;

using NUnit.Framework;

[TestFixture]
public class ShapeTests : TestBase
{
    [Test]
    public void Area_IsPositive() { new Outer.Inner.Deep.Shape<string>(4).Area(); }

    [NUnit.Framework.TestCase(1)]
    public void TestDraw(int n) => Helpers.Draw(n);

    public void NotATest() { }
}

public interface IDrawable : IDisposable { void Draw(); }
