using System;
using Fixture.Billing;
using Pay = Fixture.Billing.IPayment;

namespace Fixture.App
{
    public static class Program
    {
        public static void Main()
        {
            var invoice = new Invoice();
            invoice.AddLine(10m);
            invoice.AddLine(5m);
            Pay payment = new CardPayment();
            Console.WriteLine(payment.Charge(invoice.Total()));
        }
    }
}
