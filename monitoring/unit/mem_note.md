## Htop

被 Zswap/Zram 占用的物理内存，不应该算作应用程序的内存消耗
SReclaimable 归为 Cached

```c
static void LinuxMachine_scanMemoryInfo(LinuxMachine* this) {
   Machine* host = &this->super;
   memory_t availableMem = 0;
   memory_t freeMem = 0;
   memory_t totalMem = 0;
   memory_t buffersMem = 0;
   memory_t cachedMem = 0;
   memory_t sharedMem = 0;
   memory_t swapTotalMem = 0;
   memory_t swapCacheMem = 0;
   memory_t swapFreeMem = 0;
   memory_t sreclaimableMem = 0;
   memory_t zswapCompMem = 0;
   memory_t zswapOrigMem = 0;

   FILE* file = fopen(PROCMEMINFOFILE, "r");
   if (!file)
      CRT_fatalError("Cannot open " PROCMEMINFOFILE);

   char buffer[128];
   while (fgets(buffer, sizeof(buffer), file)) {

      #define tryRead(label, variable)                                       \
         if (String_startsWith(buffer, label)) {                             \
            memory_t parsed_;                                                \
            if (sscanf(buffer + strlen(label), "%llu kB", &parsed_) == 1) {  \
               (variable) = parsed_;                                         \
            }                                                                \
            break;                                                           \
         } else (void) 0 /* Require a ";" after the macro use. */

      switch (buffer[0]) {
         case 'M':
            tryRead("MemAvailable:", availableMem);
            tryRead("MemFree:", freeMem);
            tryRead("MemTotal:", totalMem);
            break;
         case 'B':
            tryRead("Buffers:", buffersMem);
            break;
         case 'C':
            tryRead("Cached:", cachedMem);
            break;
         case 'S':
            switch (buffer[1]) {
               case 'h':
                  tryRead("Shmem:", sharedMem);
                  break;
               case 'w':
                  tryRead("SwapTotal:", swapTotalMem);
                  tryRead("SwapCached:", swapCacheMem);
                  tryRead("SwapFree:", swapFreeMem);
                  break;
               case 'R':
                  tryRead("SReclaimable:", sreclaimableMem);
                  break;
            }
            break;
         case 'Z':
            tryRead("Zswap:", zswapCompMem);
            tryRead("Zswapped:", zswapOrigMem);
            break;
      }

      #undef tryRead
   }

   fclose(file);

   /*
    * Compute memory partition like procps(free)
    *  https://gitlab.com/procps-ng/procps/-/blob/master/proc/sysinfo.c
    *
    * Adjustments:
    *  - Shmem in part of Cached (see https://lore.kernel.org/patchwork/patch/648763/),
    *    do not show twice by subtracting from Cached and do not subtract twice from used.
    */
   host->totalMem = totalMem;
   host->cachedMem = cachedMem + sreclaimableMem - sharedMem;
   host->sharedMem = sharedMem;
   const memory_t usedDiff = freeMem + cachedMem + sreclaimableMem + buffersMem;
   host->usedMem = (totalMem >= usedDiff) ? totalMem - usedDiff : totalMem - freeMem;
   host->buffersMem = buffersMem;
   host->availableMem = availableMem != 0 ? MINIMUM(availableMem, totalMem) : freeMem;
   host->totalSwap = swapTotalMem;
   host->usedSwap = swapTotalMem - swapFreeMem - swapCacheMem;
   host->cachedSwap = swapCacheMem;
   this->zswap.usedZswapComp = zswapCompMem;
   this->zswap.usedZswapOrig = zswapOrigMem;
}



static void MemoryMeter_updateValues(Meter* this) {
   char* buffer = this->txtBuffer;
   size_t size = sizeof(this->txtBuffer);
   int written;

   Settings *settings = this->host->settings;

   /* shared, compressed and available memory are not supported on all platforms */
   this->values[MEMORY_METER_SHARED] = NAN;
   this->values[MEMORY_METER_COMPRESSED] = NAN;
   this->values[MEMORY_METER_AVAILABLE] = NAN;
   Platform_setMemoryValues(this);
   if ((this->mode == GRAPH_METERMODE || this->mode == BAR_METERMODE) && !settings->showCachedMemory) {
      this->values[MEMORY_METER_BUFFERS] = 0;
      this->values[MEMORY_METER_CACHE] = 0;
   }
   /* Do not print available memory in bar mode */
   static_assert(MEMORY_METER_AVAILABLE + 1 == MEMORY_METER_ITEMCOUNT,
      "MEMORY_METER_AVAILABLE is not the last item in MemoryMeterValues");
   this->curItems = MEMORY_METER_AVAILABLE;

   /* we actually want to show "used + shared + compressed" */
   double used = this->values[MEMORY_METER_USED];
   if (isPositive(this->values[MEMORY_METER_SHARED]))
      used += this->values[MEMORY_METER_SHARED];
   if (isPositive(this->values[MEMORY_METER_COMPRESSED]))
      used += this->values[MEMORY_METER_COMPRESSED];

   written = Meter_humanUnit(buffer, used, size);
   METER_BUFFER_CHECK(buffer, size, written);

   METER_BUFFER_APPEND_CHR(buffer, size, '/');

   Meter_humanUnit(buffer, this->total, size);
}

void Platform_setMemoryValues(Meter* this) {
   const Machine* host = this->host;
   const LinuxMachine* lhost = (const LinuxMachine*) host;

   this->total = host->totalMem;
   this->values[MEMORY_METER_USED] = host->usedMem;
   this->values[MEMORY_METER_SHARED] = host->sharedMem;
   this->values[MEMORY_METER_COMPRESSED] = 0; /* compressed */
   this->values[MEMORY_METER_BUFFERS] = host->buffersMem;
   this->values[MEMORY_METER_CACHE] = host->cachedMem;
   this->values[MEMORY_METER_AVAILABLE] = host->availableMem;

   if (lhost->zfs.enabled != 0 && !Running_containerized) {
      // ZFS does not shrink below the value of zfs_arc_min.
      unsigned long long int shrinkableSize = 0;
      if (lhost->zfs.size > lhost->zfs.min)
         shrinkableSize = lhost->zfs.size - lhost->zfs.min;
      this->values[MEMORY_METER_USED] -= shrinkableSize;
      this->values[MEMORY_METER_CACHE] += shrinkableSize;
      this->values[MEMORY_METER_AVAILABLE] += shrinkableSize;
   }

   if (lhost->zswap.usedZswapOrig > 0 || lhost->zswap.usedZswapComp > 0) {
      this->values[MEMORY_METER_USED] -= lhost->zswap.usedZswapComp;
      this->values[MEMORY_METER_COMPRESSED] += lhost->zswap.usedZswapComp;
   }
}
```
