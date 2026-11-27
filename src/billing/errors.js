export class BillingError extends Error {
  constructor(code, status = 400, details = {}) {
    super(code);
    this.name = 'BillingError';
    this.code = code;
    this.status = status;
    this.details = details;
  }
}

export function sendBillingError(reply, error) {
  if (!(error instanceof BillingError)) throw error;
  return reply.code(error.status).send({ error: error.code, ...error.details });
}
