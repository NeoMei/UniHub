import assert from 'node:assert/strict';
import test from 'node:test';
import { quote, tariffAt } from './smoke.mjs';

const usage = { prompt_cache_hit_tokens: 3, prompt_cache_miss_tokens: 8,
  prompt_tokens: 11, completion_tokens: 1, total_tokens: 12 };

test('mixed native usage produces exact off-peak and peak CNY and payer units', () => {
  assert.deepEqual(quote(usage, 'offpeak'), { period: 'offpeak', hit: 3, miss: 8, output: 1, total: 12,
    baseCny: '0.00004995', expectedUnits: '9990' });
  assert.deepEqual(quote(usage, 'peak'), { period: 'peak', hit: 3, miss: 8, output: 1, total: 12,
    baseCny: '0.00009990', expectedUnits: '19980' });
});

test('missing evidence cannot be billed as zero cache hits', () => {
  const { prompt_cache_hit_tokens, prompt_cache_miss_tokens, ...noSplit } = usage;
  assert.throws(() => quote(noSplit, 'peak'), /cache breakdown/);
  assert.deepEqual(quote({ ...noSplit, prompt_tokens_details: { cached_tokens: 3 } }, 'peak'), quote(usage, 'peak'));
  assert.throws(() => quote({ ...usage, billing_usage: { estimated: true } }, 'peak'), /estimated/);
  assert.throws(() => quote({ ...usage, prompt_cache_hit_tokens: 0 }, 'offpeak'), /cache breakdown/);
  assert.throws(() => quote({ ...usage, total_tokens: 20 }, 'offpeak'), /token counts/);
});

test('Beijing tariffs respect window boundaries, weekends and the supplied holiday calendar', () => {
  const at = (date, kind) => tariffAt(new Date(date + '+08:00'), JSON.stringify({ days: { [date.slice(0, 10)]: kind } }));
  assert.equal(at('2026-11-29T10:00:00'), 'offpeak');
  assert.equal(at('2026-11-30T08:59:59'), 'offpeak');
  assert.equal(at('2026-11-30T09:00:00', 'workday'), 'peak');
  assert.equal(at('2026-11-30T12:00:00'), 'offpeak');
  assert.equal(at('2026-11-30T14:00:00', 'workday'), 'peak');
  assert.equal(at('2026-11-30T18:00:00'), 'offpeak');
  assert.equal(at('2026-10-01T10:00:00', 'holiday'), 'offpeak');
  assert.throws(() => at('2026-11-30T10:00:00'), /CALENDAR/);
});
