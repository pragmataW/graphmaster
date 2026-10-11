using Fixture.Billing;
using Xunit;

namespace Fixture.Tests
{
    public class InvoiceTests
    {
        [Fact]
        public void TotalIncludesTax()
        {
            var invoice = new Invoice();
            invoice.AddLine(100m);
            Assert.Equal(120m, invoice.Total());
        }

        [Fact]
        public void CardPaymentAddsFee()
        {
            Assert.Equal(10.30m, new CardPayment().Charge(10m));
        }
    }
}
