import { paragraph, readerFixture } from './readerFixture';
import type { ArticleBlock } from '../src/lib/api/client';

export const longReaderFixtureID = '01J00000000000000000000LONG';

// Original synthetic feature article. Each source word has an authored gloss;
// underscores only encode spaces inside an English gloss. No runtime dictionary.
const story = [
[
`Op/On zaterdagochtend/Saturday_morning liep/walked Noor/Noor door/through haar/her buurt/neighborhood terwijl/while de/the winkels/shops langzaam/slowly opengingen./opened.`,
`Bij/At het/the oude/old station/station stond/stood een/a gebouw/building dat/that al/already jaren/years leeg/empty was/was en/and waarvan/of_which niemand/nobody de/the eigenaar/owner kende./knew.`,
`Achter/Behind de/the stoffige/dusty ramen/windows zag/saw ze/she lange/long tafels,/tables, een/a kapotte/broken lamp/lamp en/and een/a stapel/pile stoelen/chairs die/that ooit/once rood/red waren/had geweest./been.`,
`Ze/She bleef/stayed even/briefly staan/standing en/and vroeg/asked zich/herself af/off waarom/why deze/this plek/place eigenlijk/actually niet/not meer/anymore werd/was gebruikt./used.`
],
[
`Een/A week/week later/later zat/sat ze/she met/with haar/her buurman/neighbor Sam/Sam aan/at de/the keukentafel/kitchen_table om/to over/about het/the gebouw/building te/to praten./talk.`,
`Hij/He vertelde/told dat/that er/there vroeger/formerly een/a kleine/small bibliotheek/library was/was geweest/been waar/where kinderen/children na/after school/school hun/their huiswerk/homework maakten./did.`,
`Toen/When de/the gemeente/municipality een/a groter/larger pand/building opende,/opened, verhuisden/moved de/the boeken/books en/and verdwenen/disappeared ook/also de/the mensen/people uit/from de/the straat./street.`,
`Noor/Noor stelde/put het/the idee/idea van/of een/a gedeelde/shared werkplaats/workshop voor/forward en/and Sam/Sam begon/began meteen/immediately mogelijkheden/possibilities op/on papier/paper te/to zetten./put.`
],
[
`Ze/They wilden/wanted geen/no duur/expensive project/project met/with ingewikkelde/complicated regels,/rules, maar/but een/a rustige/quiet plek/place waar/where buren/neighbors elkaar/each_other konden/could helpen./help.`,
`Iemand/Someone kon/could er/there een/a fiets/bicycle repareren,/repair, een/a ander/other persoon/person kon/could leren/learn naaien/sew en/and kinderen/children mochten/were_allowed samen/together iets/something bouwen./build.`,
`Het/The belangrijkste/most_important was/was volgens/according_to Noor/Noor dat/that bezoekers/visitors niet/not eerst/first iets/something hoefden/had_to te/to kopen/buy voordat/before ze/they welkom/welcome waren./were.`,
`Sam/Sam schreef/wrote bovenaan/at_the_top zijn/his blad/sheet dat/that de/the deur/door open/open moest/had_to blijven/stay voor/for iedereen/everyone die/who nieuwsgierig/curious was./was.`
],
[
`De/The eerste/first bijeenkomst/meeting vond/took op/on een/a regenachtige/rainy donderdagavond/Thursday_evening in/in het/the buurthuis/community_center plaats/place en/and trok/attracted twaalf/twelve bezoekers./visitors.`,
`Sommigen/Some brachten/brought duidelijke/clear plannen/plans mee,/along, terwijl/while anderen/others vooral/mainly wilden/wanted weten/know wie/who de/the rekening/bill voor/for verwarming/heating zou/would betalen./pay.`,
`Een/An oudere/older vrouw/woman vroeg/asked of/whether er/there ook/also een/a hoek/corner zou/would komen/come waar/where je/you gewoon/simply koffie/coffee kon/could drinken/drink zonder/without iets/anything te/to moeten./have_to.`,
`Die/That vraag/question veranderde/changed het/the gesprek/conversation omdat/because iedereen/everyone ineens/suddenly begreep/understood dat/that samen/together zijn/being even/as belangrijk/important was/was als/as samen/together werken./working.`
],
[
`Daarna/Afterwards begon/began het/the minder/less zichtbare/visible werk/work van/of bellen,/calling, rekenen/calculating en/and wachten/waiting op/for antwoorden/answers die/that niet/not altijd/always kwamen./came.`,
`De/The eigenaar/owner woonde/lived inmiddels/by_now in/in een/a andere/other stad/city en/and wilde/wanted eerst/first precies/exactly weten/know wat/what er/there met/with zijn/his pand/building zou/would gebeuren./happen.`,
`Noor/Noor legde/laid tijdens/during een/a lang/long telefoongesprek/phone_call rustig/calmly uit/out hoe/how vrijwilligers/volunteers de/the ruimte/space zouden/would gebruiken/use en/and onderhouden./maintain.`,
`Hij/He gaf/gave het/the plan/plan dat/that zij/she samen/together met/with haar/her buren/neighbors zorgvuldig/carefully had/had uitgewerkt/developed uiteindelijk/eventually toch/after_all een/a kans./chance.`
],
[
`Op/On de/the eerste/first schoonmaakdag/cleaning_day lagen/lay overal/everywhere dozen/boxes en/and moesten/had_to de/the ramen/windows voorzichtig/carefully worden/be schoongemaakt/cleaned voordat/before er/there genoeg/enough licht/light binnenkwam./entered.`,
`Sam/Sam vond/found onder/under een/a kast/cupboard een/a kaart/map van/of de/the buurt/neighborhood waarop/on_which straten/streets stonden/stood die/that nu/now heel/very anders/differently heetten./were_named.`,
`Een/A meisje/girl wees/pointed naar/at de/the plek/place waar/where haar/her school/school stond/stood en/and ontdekte/discovered dat/that daar/there vroeger/formerly een/a tuin/garden was/was geweest./been.`,
`Ze/They hingen/hung de/the kaart/map naast/beside de/the ingang/entrance op/up zodat/so_that nieuwe/new bezoekers/visitors konden/could zien/see hoeveel/how_much een/a buurt/neighborhood kan/can veranderen./change.`
],
[
`Niet/Not alles/everything ging/went vanzelf/smoothly en/and na/after een/a paar/few weken/weeks ontstond/arose er/there ruzie/argument over/about het/the gebruik/use van/of de/the grootste/largest tafel./table.`,
`De/The mensen/people die/that fietsen/bicycles repareerden/repaired wilden/wanted hun/their gereedschap/tools laten/let liggen,/lie, maar/but de/the naaigroep/sewing_group had/had dezelfde/same ruimte/space iedere/every dinsdag/Tuesday nodig./necessary.`,
`Noor/Noor zat/sat er/there een/a avond/evening mee/with in/in haar/her maag/stomach en/and vroeg/asked zich/herself af/off of/whether ze/she te/too veel/much had/had beloofd./promised.`,
`Ze/She gaf/gave haar/her plan/plan echter/however niet/not op/up en/and nodigde/invited beide/both groepen/groups uit/out om/to samen/together een/a eenvoudige/simple afspraak/agreement te/to maken./make.`
],
[
`Uiteindelijk/Eventually bleek/turned_out een/a kast/cupboard op/on wielen/wheels genoeg/enough om/to het/the probleem/problem op/on te/to lossen/loosen zonder/without nieuwe/new regels/rules of/or extra/extra vergaderingen./meetings.`,
`Aan/At het/the einde/end van/of elke/every middag/afternoon verdwenen/disappeared de/the spullen/things in/into de/the kast/cupboard en/and werd/became de/the tafel/table weer/again vrij./free.`,
`Dat/That kleine/small succes/success gaf/gave iedereen/everyone vertrouwen/confidence omdat/because het/it liet/let zien/see dat/that een/a verschil/difference van/of mening/opinion niet/not meteen/immediately een/a ramp/disaster was./was.`,
`Sam/Sam schreef/wrote de/the afspraak/agreement op/on een/a kaartje/card en/and hing/hung dat/that boven/above de/the kast/cupboard waar/where niemand/nobody het/it kon/could missen./miss.`
],
[
`Toen/When de/the lente/spring begon,/began, stond/stood de/the deur/door op/on zonnige/sunny dagen/days open/open en/and bleven/stayed voorbijgangers/passersby steeds/ever vaker/more_often even/briefly kijken./looking.`,
`Een/A vader/father kwam/came met/with zijn/his zoon/son langs/by om/to een/a houten/wooden vogelhuisje/birdhouse te/to maken/make voor/for de/the boom/tree achter/behind hun/their huis./house.`,
`Een/A student/student hielp/helped een/a buurvrouw/neighbor met/with haar/her telefoon/phone en/and kreeg/received daarna/afterwards een/a recept/recipe voor/for soep/soup dat/that haar/her moeder/mother altijd/always maakte./made.`,
`Niemand/Nobody hield/kept precies/exactly bij/up hoeveel/how_many problemen/problems er/there werden/were opgelost,/solved, maar/but op/on vrijdag/Friday was/was de/the koffiepot/coffee_pot meestal/usually leeg./empty.`
],
[
`Toch/Still bleef/remained geld/money een/a onderwerp/topic waarover/about_which ze/they eerlijk/honestly moesten/had_to praten/talk omdat/because enthousiasme/enthusiasm de/the elektriciteitsrekening/electricity_bill niet/not kon/could betalen./pay.`,
`De/The groep/group besloot/decided een/a kleine/small vrijwillige/voluntary bijdrage/contribution te/to vragen/ask en/and iedere/every maand/month zichtbaar/visibly te/to maken/show waaraan/on_what het/the geld/money werd/was besteed./spent.`,
`Wie/Whoever niets/nothing kon/could missen/spare bleef/stayed net/just zo/as welkom/welcome en/and kon/could eventueel/possibly helpen/help met/with opruimen/tidying of/or het/the openen/opening van/of de/the deur./door.`,
`Voor/For Noor/Noor was/was die/that afspraak/agreement belangrijker/more_important dan/than een/a volle/full kas/cashbox omdat/because vertrouwen/trust volgens/according_to haar/her begon/began bij/with duidelijkheid./clarity.`
],
[
`Zes/Six maanden/months na/after de/the eerste/first bijeenkomst/meeting organiseerden/organized de/the buren/neighbors een/a middag/afternoon waarop/on_which iedereen/everyone iets/something mocht/could laten/let zien/show dat/that hier/here was/was gemaakt./made.`,
`Er/There stonden/stood gerepareerde/repaired lampen,/lamps, zelfgemaakte/homemade tassen/bags en/and een/a scheve/crooked boekenplank/bookshelf die/that volgens/according_to de/the maker/maker juist/precisely daardoor/thereby bijzonder/special was./was.`,
`De/The oude/old kaart/map hing/hung nog/still naast/beside de/the ingang/entrance maar/but inmiddels/by_now zaten/were er/there kleine/small briefjes/notes omheen/around_it met/with herinneringen/memories van/of bezoekers./visitors.`,
`Het/The gebouw/building vertelde/told niet/not langer/longer alleen/only een/a verhaal/story over/about vroeger,/the_past, maar/but ook/also over/about de/the mensen/people die/that er/there nu/now samenkwamen./gathered.`
],
[
`Toen/When de/the laatste/last bezoeker/visitor vertrok,/left, ging/went Noor/Noor op/on de/the bank/sofa bij/by het/the raam/window zitten/sit om/to tot/to rust/rest te/to komen./come.`,
`Buiten/Outside reden/rode fietsers/cyclists voorbij/past en/and aan/across de/the overkant/other_side deed/did iemand/someone de/the lichten/lights van/of een/a winkel/shop uit./off.`,
`Ze/She dacht/thought aan/about de/the ochtend/morning waarop/on_which ze/she voor/for het/the eerst/first door/through de/the stoffige/dusty ramen/windows had/had gekeken/looked en/and glimlachte./smiled.`,
`De/The buurt/neighborhood was/was niet/not ineens/suddenly helemaal/completely veranderd,/changed, maar/but er/there was/was nu/now een/a plek/place waar/where een/a onbekende/stranger langzaam/slowly een/a bekende/acquaintance kon/could worden./become.`
]
];

// Explicit members, not the whole intervening source span.
const expressions: Record<number, { members: string[]; label: string; meaning: string; split?: boolean }[]> = {
  3: [{ members: ['vroeg', 'zich', 'af'], label: 'zich afvragen', meaning: 'wonder', split: true }],
  7: [{ members: ['stelde', 'voor'], label: 'stelde … voor', meaning: 'proposed', split: true }],
  12: [{ members: ['vond', 'plaats'], label: 'vond … plaats', meaning: 'took place', split: true }],
  18: [{ members: ['legde', 'uit'], label: 'legde … uit', meaning: 'explained', split: true }],
  23: [{ members: ['hingen', 'op'], label: 'hingen … op', meaning: 'hung up', split: true }],
  26: [{ members: ['zat', 'er', 'mee', 'in', 'haar', 'maag'], label: 'ergens mee in je maag zitten', meaning: 'be troubled by something', split: true }, { members: ['vroeg', 'zich', 'af'], label: 'zich afvragen', meaning: 'wonder', split: true }],
  27: [{ members: ['gaf', 'op'], label: 'gaf … op', meaning: 'gave up', split: true }, { members: ['nodigde', 'uit'], label: 'nodigde … uit', meaning: 'invited', split: true }],
  28: [{ members: ['op', 'te', 'lossen'], label: 'op te lossen', meaning: 'to solve' }],
  35: [{ members: ['hield', 'bij'], label: 'hield … bij', meaning: 'kept track of', split: true }],
  44: [{ members: ['tot', 'rust', 'te', 'komen'], label: 'tot rust te komen', meaning: 'to unwind' }],
  45: [{ members: ['deed', 'uit'], label: 'deed … uit', meaning: 'switched off', split: true }]
};

let sentenceIndex = 0;
const blocks: ArticleBlock[] = story.map((sentences, blockIndex) => {
  let offset = 0;
  const parts = sentences.map((tagged) => {
    const pairs = tagged.trim().split(/\s+/).map(pair => pair.split('/'));
    if (pairs.some(pair => pair.length !== 2 || !pair[1])) throw new Error('Long fixture: invalid word/gloss pair');
    const source = pairs.map(pair => pair[0]).join(' ');
    const words = pairs.map(pair => pair[0]!.replace(/[.,]/g, ''));
    const groups = (expressions[sentenceIndex] || []).map(expression => {
      const contiguousStart = expression.split ? -1 : words.findIndex((_word, index) => expression.members.every((member, delta) => words[index + delta] === member));
      if (!expression.split && contiguousStart < 0) throw new Error(`Missing contiguous expression: ${expression.label}`);
      let after = -1;
      const members = expression.members.map((member, index) => {
        if (!expression.split) return contiguousStart + index;
        after = words.findIndex((word, index) => index > after && word === member);
        if (after < 0) throw new Error(`Missing expression member: ${member}`);
        return after;
      });
      return { ...expression, words: members };
    });
    const part = paragraph(sentenceIndex++, source, pairs.map(pair => pair[1]!.replaceAll('_', ' ').replace(/[.,]$/, '')), groups);
    const blockID = `long-block-${blockIndex}`;
    const rebased = { ...part, id: blockID, article_id: longReaderFixtureID, block_index: blockIndex,
      sentences: part.sentences!.map(sentence => ({ ...sentence, article_block_id: blockID, start_utf16: sentence.start_utf16 + offset, end_utf16: sentence.end_utf16 + offset })),
      occurrences: part.occurrences!.map(occurrence => ({ ...occurrence, article_block_id: blockID, spans: occurrence.spans.map(span => ({ ...span, start_utf16: span.start_utf16 + offset, end_utf16: span.end_utf16 + offset })) })) };
    offset += source.length + 1;
    return rebased;
  });
  return { ...parts[0]!, source_text: parts.map(part => part.source_text).join(' '), sentences: parts.flatMap(part => part.sentences), occurrences: parts.flatMap(part => part.occurrences) };
});

export const longReaderFixture = { ...readerFixture, id: longReaderFixtureID, title: 'Een open deur voor de buurt', blocks,
  sentences: blocks.flatMap(block => block.sentences), occurrences: blocks.flatMap(block => block.occurrences),
  analysis_progress: { total_paragraphs: blocks.length, completed_paragraphs: blocks.length, current_block_index: -1, failed_block_index: -1 },
  narration: { ...readerFixture.narration!, sentence_count: sentenceIndex }
};
