namespace Fixture.Billing
{
    public abstract class PaymentBase
    {
        protected decimal Fee { get; init; } = 0.30m;

        protected decimal WithFee(decimal amount) => amount + Fee;
    }
}
