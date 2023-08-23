const assert = require("assert")

module.exports = async page => {
    console.log("waiting for service element")
    await page.waitForSelector(".span-svc-name")
    await (await page.$(".TimelineCollapser--btn-expand")).click()

    const services = await page.$$(".span-svc-name")

    async function assertObjectName(svcNameEl, service, operation) {
        assert.match(await svcNameEl.evaluate(el => el.textContent.trim()), service)
        const endpointNameEl = await (await svcNameEl.getProperty("parentElement")).$(".endpoint-name")
        assert.match(await endpointNameEl.evaluate(el => el.textContent.trim()), operation)
    }

    await assertObjectName(services[0], /^tracetest deployments$/, /^demo \/ \d\d:\d\d:\d\d\.\.\d\d:\d\d:\d\d$/)
    await assertObjectName(services[1], /^replicasets$/, /^demo-[0-9a-f]+$/)
    await assertObjectName(services[2], /^pods$/, /^demo-[0-9a-f]+-[0-9a-z]{5}$/)

    console.log("waiting for log header element")
    const logHeader = await page.waitForSelector(".AccordianLogs--header")
    await logHeader.click()
}
