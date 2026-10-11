namespace Fixture.Billing
{
    public partial class Invoice
    {
        public const decimal TaxRate = 0.2m;

        private decimal ApplyTax(decimal net)
        {
            return decimal.Round(net * (1 + TaxRate), 2);
        }
    }
}
