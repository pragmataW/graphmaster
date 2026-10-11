using System.Collections.Generic;
using System.Linq;

namespace Fixture.Billing
{
    public partial class Invoice
    {
        private readonly List<decimal> _lines = new();

        public void AddLine(decimal amount) => _lines.Add(amount);

        public decimal Total()
        {
            return ApplyTax(_lines.Sum());
        }
    }
}
