namespace Fixture.Billing;

public interface IPayment
{
    decimal Charge(decimal amount);
}
