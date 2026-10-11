namespace Fixture.Billing
{
    public class CardPayment : PaymentBase, IPayment
    {
        public decimal Charge(decimal amount)
        {
            return WithFee(amount);
        }
    }
}
