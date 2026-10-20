# Large-target metrics sizing

Manual dev-cluster measurements from 2026-09-15, using Collector 0.159.0,
a single Kind node, four VM CPUs, and 32 GiB VM memory.

## Workload and method

Two ServiceMonitor endpoints each returned 1,000,000 distinct gauge samples.
Each response contained 255,000,000 uncompressed bytes, about 243 MiB, with
12 source labels per sample. Both targets were assigned to the same Collector.
A barrier started their responses within milliseconds, every 120 seconds.

A temporary OTLP HTTP sink decoded exported protobuf batches and counted unique
sample identifiers. After complete delivery, the sink returned HTTP 503 for
six minutes while scraping continued. New scrapes were then stopped and the
sink resumed accepting exports. Container cgroup memory peaks, OOM counters,
Collector RSS, and queue and delivery metrics were recorded.

## Test configuration

The initial results below use a 1.5 GiB queue and 256 MiB body limit.
Additional measurements with a 2 GiB queue and 1 GiB body limit appear under
mixed sample lengths below. Both are measurement configurations. The chart
defaults are a 128 MiB queue and 1 GiB body limit. See
[Large-target metrics](../../docs/components/large_target_metrics.md).

| Setting | Value |
| --- | --- |
| Memory request / limit | 8 GiB / 16 GiB |
| GOMEMLIMIT / GOGC | 8 GiB / 50 |
| Memory limiter hard / soft heap thresholds | 12.8 GiB / 8.8 GiB |
| Exporter queue | 1.5 GiB serialized bytes, 32 consumers |
| Queue batch minimum / maximum | 1 MiB / 2 MiB |
| Samples / response body | 1,000,000 / 256 MiB |
| Labels / label name / label value | 64 / 256 bytes / 4096 bytes |

## Results

- Both endpoints delivered all 1,000,000 unique samples. Normal delivery had no
  exporter queue admission failures.
- During the six-minute outage, the queue retained about 1.38 GiB of serialized
  data. Additional scrapes were refused by the queue or memory limiter.
- The container memory peak was 11.79 GiB. Sampled process RSS peaked at
  11.84 GiB. The container recorded zero OOM events and zero restarts.
- After exports resumed and targets were removed, the queue drained in about
  355 seconds. Accepted and exported counters both reached 7,006,650 metric
  points, including scrape metadata and target-removal records.
- A 2 GiB queue with the same Go memory target reached a 13.74 GiB container
  peak. The 1.5 GiB queue preserves more memory for overlapping scrape work.
- Limit enforcement passed through Target Allocator discovery. The valid control
  delivered all 1,000 samples. Responses with 1,000,001 samples, a 705 MB body,
  too many labels, a 257-byte label name, or a 4097-byte label value returned
  `up=0` and delivered no payload samples.
- With the normal exporter restored, a separate 1,000,000-sample, 55 MB response
  passed through the metrics gateway into the default backend. Prometheus
  reported 1,000,000 series for the test target and `up=1`.

## Mixed sample lengths

These tests used the 2 GiB queue, 1 GiB body limit, and the same memory settings.
Each pair contained two concurrent responses of 1,000,000 samples each,
assigned to one Collector. The near-cap pair followed the smaller mixed pair
without restarting the Collector.

| Sample share | Mixed response | Near-cap response |
| --- | --- | --- |
| 95% | 255 bytes | 255 bytes |
| 4% | 2303 bytes | 2303 bytes |
| 1% | 4351 bytes | 73937 bytes |
| Total response | 377,880,000 bytes | 1,073,740,000 bytes |

Lengths include the full text sample. The near-cap tail uses multiple labels
with 4096-byte values, remaining within the per-label limits. Each near-cap
response is 1824 bytes below the 1 GiB body limit.

- Both mixed responses delivered all samples with `up=1`, no queue admission
  failures, and no memory-limiter refusals. Container memory peaked at 8.00 GiB.
- The near-cap pair reached 14.13 GiB. The memory limiter refused both scrapes
  before queue admission. The logs reported `data refused due to high memory usage`.
- A second near-cap pair was already in flight when discovery was disabled.
  It caused `OOMKilled` under the 16 GiB container limit. Repeated concurrent
  responses near the 1 GiB ceiling are not safe with these memory settings.
  The body limit permits responses that exceed the tested processing capacity.
- Rejection checks passed after recovery. A response with 1,000,001 samples
  remained 1569 bytes below the body cap but was rejected by the sample limit.
  A 1,000,000-sample response exceeded the body cap by 8176 bytes and was also
  rejected. Both returned `up=0` and exported no payload samples. The valid
  control delivered all 1,000 samples with `up=1`.

## Scope

These measurements cover gauge samples with the stated label structure and
concurrency. Native histograms, label churn, more targets, and shorter scrape
intervals can require more memory. The limits are scrape defaults and can be
overridden by monitoring resources. They do not establish a universal OOM bound.
The test sink limits export throughput, so its drain time is not a production
throughput measurement. The dev cluster lacked the resource metrics API, so
autoscaling was not tested.
