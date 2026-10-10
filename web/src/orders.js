export function olderUnpaid(order, now = Date.now()) {
  const empty = ["awaiting_address", "awaiting_payment", "expired"].includes(
    order.status,
  );
  const received = [
    order.bitcoinReceived,
    order.moneroReceived,
    order.nockReceived,
  ].some((value) => value && /[1-9]/.test(value));
  const paid = order.paidAt && !order.paidAt.startsWith("0001-");
  return (
    empty &&
    !paid &&
    !received &&
    !order.txHash &&
    now - Date.parse(order.created) > 2 * 60 * 60 * 1000
  );
}
