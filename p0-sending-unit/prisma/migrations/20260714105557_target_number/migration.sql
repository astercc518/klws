-- CreateTable
CREATE TABLE "TargetNumber" (
    "id" TEXT NOT NULL,
    "raw" TEXT NOT NULL,
    "e164" TEXT,
    "status" TEXT NOT NULL,
    "jid" TEXT,

    CONSTRAINT "TargetNumber_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
CREATE UNIQUE INDEX "TargetNumber_e164_key" ON "TargetNumber"("e164");
